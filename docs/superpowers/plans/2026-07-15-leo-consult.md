# leo_consult Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Any supervised Leo agent can dispatch a one-off headless subagent on any template (any harness/model) via a new `leo_consult` MCP tool; the answer is injected back into the caller's session as a framed message.

**Architecture:** A new `internal/consult` package owns dispatch: validate template/harness/model synchronously, then run the harness binary headlessly in a goroutine (one-shot `LaunchSpec`, `Kind: KindTask`, no tmux, no MCP wiring, no channels), parse the output with the adapter's `ParseEvents`, and hand the framed reply to a delivery callback. `internal/web` hosts the dispatcher behind a new bearer-authed `POST /api/consult` endpoint and supplies delivery via the readiness-probing `injectPrompt` path (resuming suspended callers, routing non-claude callers through their SessionDriver). `internal/mcp` adds the `leo_consult` tool that forwards to the endpoint with the caller's process name.

**Tech Stack:** Go, existing packages only — `internal/harness`, `internal/config`, `internal/agent`, `internal/web`, `internal/mcp`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-15-leo-consult-design.md`

## Global Constraints

- Consultant runs in the **caller's workspace** (from the agent record), never the template's.
- Reply framing: success `[consult <id> · <harness>/<model> · <elapsed>] <text>`; failure `[consult <id> · <harness>/<model> · failed after <elapsed>] <detail>`. A consult is **never silently dropped** — failures and timeouts deliver an error notice (delivery failures themselves are logged).
- Timeout: 10 minutes per consult (constant). Concurrency: max 4 consults running at once (constant); excess dispatches queue on the semaphore, they are not rejected.
- No new config surface. No consultant MCP wiring (the consultant gets no leo tools), no channels, no session persistence.
- Model override must pass the resolved harness's `ValidateModel`; the harness must support `harness.KindTask`.
- Advisory preamble is prepended to the prompt (works on all harnesses, unlike `SystemContext` which opencode ignores).
- All commits follow `<type>: <description>` (feat/test/docs/chore). Run `go test -race ./...` before each commit; `make lint` and `make e2e` before push.
- `cmd.WaitDelay` must be set on the consult exec so a cancelled run can't hang `Wait()` on inherited pipes (macOS CI lesson).

---

### Task 1: `internal/consult` dispatcher package

**Files:**
- Create: `internal/consult/consult.go`
- Test: `internal/consult/consult_test.go`

**Interfaces:**
- Consumes: `config.Config` (`Templates`, `TemplateHarness`, `TemplateModel`, `TemplateHarnessOptions` cascades), `harness.Get/LaunchSpec/Result/KindTask`.
- Produces (used by Task 3):
  - `type Request struct { From, Template, Model, Prompt, Workspace string }`
  - `type Ticket struct { ID, Harness, Model string }`
  - `type DeliverFunc func(ctx context.Context, agentName, body string) error`
  - `func NewDispatcher(deliver DeliverFunc) *Dispatcher`
  - `func (d *Dispatcher) Dispatch(cfg *config.Config, req Request) (Ticket, error)` — synchronous validation, async run.
  - Test seam: `Dispatcher.execCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd` (exported enough for `internal/web` tests via a setter or by placing web tests' stubbing behind the handler seam — keep it a package-level-visible struct field `ExecCommandContext` so web tests can replace it).

- [ ] **Step 1: Write the failing tests**

```go
package consult

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Templates: map[string]config.TemplateConfig{
			"gpt": {Harness: "claude"}, // claude adapter: easiest ParseEvents fixture
		},
	}
}

func echoResult(text string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"`+text+`","is_error":false}`)
	}
}

func TestDispatchUnknownTemplate(t *testing.T) {
	d := NewDispatcher(func(context.Context, string, string) error { return nil })
	_, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "nope", Prompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected unknown-template error, got %v", err)
	}
}

func TestDispatchInvalidModelOverride(t *testing.T) {
	d := NewDispatcher(func(context.Context, string, string) error { return nil })
	_, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Model: "gpt-9-nano", Prompt: "q"})
	if err == nil {
		t.Fatal("expected model validation error")
	}
}

func TestDispatchRunsAndDeliversReply(t *testing.T) {
	got := make(chan struct {
		name string
		body string
	}, 1)
	d := NewDispatcher(func(_ context.Context, name, body string) error {
		got <- struct {
			name string
			body string
		}{name, body}
		return nil
	})
	d.ExecCommandContext = echoResult("opinion text")

	tk, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Prompt: "q", Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if tk.ID == "" || tk.Harness != "claude" {
		t.Fatalf("unexpected ticket %+v", tk)
	}

	select {
	case r := <-got:
		if r.name != "caller" {
			t.Fatalf("delivered to %q, want caller", r.name)
		}
		if !strings.Contains(r.body, "opinion text") || !strings.Contains(r.body, "[consult "+tk.ID) {
			t.Fatalf("unexpected reply body: %q", r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never delivered")
	}
}

func TestDispatchFailureDeliversErrorNotice(t *testing.T) {
	got := make(chan string, 1)
	d := NewDispatcher(func(_ context.Context, _, body string) error {
		got <- body
		return nil
	})
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	tk, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Prompt: "q", Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "failed after") || !strings.Contains(body, tk.ID) {
			t.Fatalf("unexpected failure body: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure notice never delivered")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/consult/`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Implement `internal/consult/consult.go`**

```go
// Package consult dispatches one-off headless "second opinion" subagents:
// a single harness -p style run on a template's harness/model, whose final
// text is delivered back to the calling agent as an injected message.
package consult

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

const (
	// runTimeout bounds one consultant run end to end.
	runTimeout = 10 * time.Minute
	// deliverTimeout bounds reply injection (readiness probe + possible
	// caller resume — a cold-booting claude can take ~60s).
	deliverTimeout = 3 * time.Minute
	// maxConcurrent caps simultaneously running consults so a council
	// fan-out can't fork-bomb a local model server. Excess dispatches
	// queue; they are never rejected.
	maxConcurrent = 4
	// preamble frames the consultant's role. Prepended to the prompt (not
	// SystemContext) so it reaches every harness, including opencode.
	preamble = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."
)

// Request describes one consult dispatch.
type Request struct {
	From      string // calling agent name; the reply target
	Template  string // template supplying harness/model/env/harness_options
	Model     string // optional model override
	Prompt    string // self-contained question
	Workspace string // caller's workspace (resolved by the caller of Dispatch)
}

// Ticket identifies an accepted consult.
type Ticket struct {
	ID      string
	Harness string
	Model   string
}

// DeliverFunc injects the framed reply body into the named agent's session.
type DeliverFunc func(ctx context.Context, agentName, body string) error

// Dispatcher validates and runs consults. Safe for concurrent use.
type Dispatcher struct {
	deliver DeliverFunc
	sem     chan struct{}

	// ExecCommandContext is the exec seam; tests replace it.
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewDispatcher builds a Dispatcher that hands replies to deliver.
func NewDispatcher(deliver DeliverFunc) *Dispatcher {
	return &Dispatcher{
		deliver:            deliver,
		sem:                make(chan struct{}, maxConcurrent),
		ExecCommandContext: exec.CommandContext,
	}
}

// Dispatch validates the consult synchronously and launches it in the
// background. The returned Ticket's ID appears in the eventual reply frame.
func (d *Dispatcher) Dispatch(cfg *config.Config, req Request) (Ticket, error) {
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		return Ticket{}, fmt.Errorf("unknown template %q", req.Template)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return Ticket{}, fmt.Errorf("resolving harness for template %q: %w", req.Template, err)
	}
	if !h.SupportsKind(harness.KindTask) {
		return Ticket{}, fmt.Errorf("harness %q does not support one-shot runs", h.Name())
	}
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Ticket{}, fmt.Errorf("model for consult: %w", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Ticket{}, fmt.Errorf("template %q harness_options: %w", req.Template, err)
	}

	id, err := newID()
	if err != nil {
		return Ticket{}, fmt.Errorf("generating consult id: %w", err)
	}

	// Runtime-only option fields (MCP wiring, channel prefixes) are left
	// zero deliberately: the consultant is advisory and gets no leo tools.
	spec := harness.LaunchSpec{
		Kind:      harness.KindTask,
		Name:      "consult-" + id,
		Model:     model,
		Workspace: req.Workspace,
		Prompt:    preamble + "\n\n" + req.Prompt,
		Options:   decoded,
	}
	args, err := h.Args(spec)
	if err != nil {
		return Ticket{}, fmt.Errorf("building %s args: %w", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return Ticket{}, fmt.Errorf("building %s env: %w", h.Name(), err)
	}

	tk := Ticket{ID: id, Harness: h.Name(), Model: model}
	go d.run(tk, h, req, args, mergeEnv(harnessEnv, tmpl.Env))
	return tk, nil
}

// run executes one consult and delivers its reply. Always delivers
// something — success text, error notice, or timeout notice.
func (d *Dispatcher) run(tk Ticket, h harness.Harness, req Request, args []string, extraEnv map[string]string) {
	d.sem <- struct{}{}
	defer func() { <-d.sem }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	binary := h.Binary()
	if p, err := exec.LookPath(binary); err == nil {
		binary = p
	}
	cmd := d.ExecCommandContext(ctx, binary, args...)
	cmd.Dir = req.Workspace
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	// A cancelled consultant may leave children holding the output pipes;
	// WaitDelay bounds how long Wait blocks on them after ctx fires.
	cmd.WaitDelay = 10 * time.Second

	out, runErr := cmd.CombinedOutput()
	elapsed := time.Since(start).Round(time.Second)

	res, parseErr := h.ParseEvents(bytes.NewReader(out))
	body := formatReply(tk, elapsed, res, ctx.Err(), runErr, parseErr)

	dctx, dcancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer dcancel()
	if err := d.deliver(dctx, req.From, body); err != nil {
		log.Printf("consult %s: delivering reply to %q failed: %v", tk.ID, req.From, err)
	}
}

// formatReply renders the framed reply. Failure detail preference order:
// timeout, run error (with stream errors if any), parse failure, empty text.
func formatReply(tk Ticket, elapsed time.Duration, res harness.Result, ctxErr, runErr, parseErr error) string {
	frame := fmt.Sprintf("[consult %s · %s/%s · %s]", tk.ID, tk.Harness, tk.Model, elapsed)
	fail := func(detail string) string {
		return fmt.Sprintf("[consult %s · %s/%s · failed after %s] %s", tk.ID, tk.Harness, tk.Model, elapsed, detail)
	}
	switch {
	case ctxErr != nil:
		return fail("timed out")
	case runErr != nil:
		detail := runErr.Error()
		if len(res.Errors) > 0 {
			detail += ": " + res.Errors[0]
		}
		return fail(detail)
	case parseErr != nil:
		return fail("unreadable output: " + parseErr.Error())
	case res.IsError:
		detail := "consultant reported an error"
		if len(res.Errors) > 0 {
			detail = res.Errors[0]
		}
		return fail(detail)
	case res.Text == "":
		return fail("consultant produced no output")
	}
	return frame + " " + res.Text
}

// mergeEnv overlays later maps onto earlier ones; nil when empty.
func mergeEnv(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// newID returns a short random consult id like "c-4f2a".
func newID() (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "c-" + hex.EncodeToString(b), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/consult/`
Expected: PASS (4 tests).

- [ ] **Step 5: Vet + commit**

```bash
go vet ./internal/consult/ && gofmt -l internal/consult/
git add internal/consult/
git commit -m "feat: add internal/consult one-off subagent dispatcher"
```

---

### Task 2: reply delivery on the web server

**Files:**
- Create: `internal/web/handlers_consult.go` (delivery half)
- Test: `internal/web/handlers_consult_test.go`

**Interfaces:**
- Consumes: existing `Server` fields — `resolveMessageTarget` (`handlers_agent_control.go:211`), `processes.States()`, `agentSvc.Resume`, `injectPrompt`, `agent.SessionName`.
- Produces (used by Task 3): `func (s *Server) deliverConsultReply(ctx context.Context, name, body string) error` — matches `consult.DeliverFunc`.

Delivery deliberately uses the readiness-probing `injectPrompt` for **both** live and resumed claude callers (unlike `handleWebAgentMessage`'s live fast-path): a consult reply lands minutes later, when the caller may be mid-turn, and the probe waits for input readiness instead of pasting blind.

- [ ] **Step 1: Write the failing tests**

Append to a new `internal/web/handlers_consult_test.go` (reuse `newTestServerWithAgents` from `handlers_agents_test.go:169`; its mock records include `leo-coding-leo` and its process states include `assistant`):

```go
package web

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func TestDeliverConsultReplyLiveAgent(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	var gotSession, gotBody string
	s.injectPrompt = func(_ context.Context, session, body string) error {
		gotSession, gotBody = session, body
		return nil
	}
	// "assistant" is live in mockProcesses.states.
	if err := s.deliverConsultReply(context.Background(), "assistant", "[consult c-1] hi"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if gotSession != agent.SessionName("assistant") || gotBody != "[consult c-1] hi" {
		t.Fatalf("injected (%q, %q)", gotSession, gotBody)
	}
}

func TestDeliverConsultReplyResumesSuspendedCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	var gotSession string
	s.injectPrompt = func(_ context.Context, session, _ string) error {
		gotSession = session
		return nil
	}
	// "leo-coding-leo" exists as a record but is NOT in processes.States()
	// → delivery must resume it first, then inject.
	if err := s.deliverConsultReply(context.Background(), "leo-coding-leo", "body"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if !svc.resumeCalled || svc.resumeName != "leo-coding-leo" {
		t.Fatalf("expected resume of caller, got %+v", svc)
	}
	if gotSession != agent.SessionName("leo-coding-leo") {
		t.Fatalf("injected into %q", gotSession)
	}
}

func TestDeliverConsultReplyUnknownCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.resumeErr = &agent.ErrNotFound{Query: "ghost"}
	if err := s.deliverConsultReply(context.Background(), "ghost", "body"); err == nil {
		t.Fatal("expected error for unknown caller")
	}
}
```

Note: if `mockAgentService.resumeErr` / `resumeCalled` / `resumeName` fields don't exist exactly as named, check `handlers_agents_test.go:157` — they do (`Resume` sets `resumeCalled`, `resumeName`, returns `resumeErr`). `ResolveHandle` on the mock returns ok=false by default, so claude-path branching is exercised.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestDeliverConsultReply ./internal/web/`
Expected: FAIL — `deliverConsultReply` undefined.

- [ ] **Step 3: Implement delivery in `internal/web/handlers_consult.go`**

```go
package web

import (
	"context"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// deliverConsultReply injects a consult reply into the calling agent's
// session. Unlike handleWebAgentMessage's live fast-path, claude callers
// always go through the readiness-probing injectPrompt: the reply arrives
// minutes after dispatch, when the caller may be mid-turn, and the probe
// waits for the input box instead of pasting blind. Suspended callers are
// resumed first; non-claude callers are routed through their SessionDriver.
func (s *Server) deliverConsultReply(ctx context.Context, name, body string) error {
	if harnessName, handle, ok := s.resolveMessageTarget(name); ok && harnessName != "" && harnessName != "claude" {
		hd, err := harness.Get(harnessName)
		if err != nil {
			return fmt.Errorf("resolving harness %q: %w", harnessName, err)
		}
		drv := hd.Driver()
		if drv == nil {
			return fmt.Errorf("harness %q has no session driver", harnessName)
		}
		_, err = drv.Inject(ctx, handle, body)
		return err
	}

	if _, live := s.processes.States()[name]; !live {
		if s.agentSvc == nil {
			return fmt.Errorf("caller %q is not running and agent service is unavailable", name)
		}
		rec, err := s.agentSvc.Resume(name)
		if err != nil {
			return fmt.Errorf("caller %q is gone: %w", name, err)
		}
		return s.injectPrompt(ctx, agent.SessionName(rec.Name), body)
	}
	return s.injectPrompt(ctx, agent.SessionName(name), body)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run TestDeliverConsultReply ./internal/web/`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
go test -race ./internal/web/
git add internal/web/handlers_consult.go internal/web/handlers_consult_test.go
git commit -m "feat: consult reply delivery via readiness-probing injection"
```

---

### Task 3: `POST /api/consult` endpoint + dispatcher wiring

**Files:**
- Modify: `internal/web/web.go` (Server struct ~line 70, `New()` ~line 147, apiMux routes ~line 265)
- Modify: `internal/web/handlers_consult.go` (add handler)
- Test: `internal/web/handlers_consult_test.go` (extend)

**Interfaces:**
- Consumes: `consult.NewDispatcher`, `consult.Request`, `consult.Ticket`, `Dispatcher.Dispatch`, `Dispatcher.ExecCommandContext` (Task 1); `s.deliverConsultReply` (Task 2); existing `s.loadConfig()`, `s.agentSvc.Resolve`, `writeJSON`, `apiResponse`.
- Produces (used by Task 4): `POST /api/consult` accepting `{"from","template","model","prompt"}`, responding `{"ok":true,"data":{"id","harness","model"}}`, errors 400/503 in the `apiResponse` envelope.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/handlers_consult_test.go`:

```go
func TestAPIConsultDispatchesAndDelivers(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	delivered := make(chan string, 1)
	s.injectPrompt = func(_ context.Context, _, body string) error {
		delivered <- body
		return nil
	}
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"gpt says yes","is_error":false}`)
	}

	// The test config (testConfigWithTemplatesYAML) must contain a template
	// named "coding" — check the fixture; if its template has another name,
	// use that name here and below.
	body := `{"from":"assistant","template":"coding","prompt":"opinion?"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Data.ID == "" {
		t.Fatalf("bad response %s (err %v)", w.Body.String(), err)
	}

	select {
	case got := <-delivered:
		if !strings.Contains(got, "gpt says yes") || !strings.Contains(got, resp.Data.ID) {
			t.Fatalf("unexpected delivery %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never delivered")
	}
}

func TestAPIConsultRejectsUnknownCaller(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	body := `{"from":"ghost","template":"coding","prompt":"q"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestAPIConsultRejectsUnknownTemplate(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	body := `{"from":"assistant","template":"nope","prompt":"q"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}
```

Add the needed imports (`encoding/json`, `net/http`, `net/http/httptest`, `os/exec`, `strings`, `time`). Caller resolution note: `"assistant"` is live in `mockProcesses.states` but has **no agent record**; the handler below therefore resolves the workspace via `agentSvc.Resolve` *or* falls back to live-process presence with an empty workspace — see Step 3, which accepts a live config-defined process whose record is missing and uses the record workspace only when available. `"ghost"` is neither a record nor live → 400.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestAPIConsult ./internal/web/`
Expected: FAIL — `s.consults` / `handleAPIConsult` undefined.

- [ ] **Step 3: Implement wiring + handler**

In `internal/web/web.go`:

1. Import `"github.com/blackpaw-studio/leo/internal/consult"`.
2. Add to the `Server` struct (after `resolveHandle`, ~line 121):

```go
	// consults dispatches one-off consultant subagents (leo_consult). Its
	// reply path is s.deliverConsultReply; tests reach through it to stub
	// the exec seam.
	consults *consult.Dispatcher
```

3. In `New()` after `s.fetchAgentListFn = s.fetchAgentList`:

```go
	s.consults = consult.NewDispatcher(s.deliverConsultReply)
```

4. Register the route next to the other API routes (~line 275):

```go
	apiMux.HandleFunc("POST /api/consult", s.handleAPIConsult)
```

In `internal/web/handlers_consult.go`, add:

```go
// handleAPIConsult dispatches a one-off consultant subagent for a calling
// agent. Validation errors return synchronously; the consultant's answer is
// delivered later via deliverConsultReply.
//
// POST /api/consult {"from": "...", "template": "...", "model": "...", "prompt": "..."}
func (s *Server) handleAPIConsult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From     string `json:"from"`
		Template string `json:"template"`
		Model    string `json:"model,omitempty"`
		Prompt   string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.From == "" || req.Template == "" || req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "from, template, and prompt are required"})
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}

	// The caller must be a supervised agent: it needs a live (or resumable)
	// session for the reply to land in. Its workspace becomes the
	// consultant's working directory when known.
	workspace := ""
	if s.agentSvc != nil {
		if rec, err := s.agentSvc.Resolve(req.From); err == nil {
			workspace = rec.Workspace
		}
	}
	_, live := s.processes.States()[req.From]
	if workspace == "" && !live {
		writeJSON(w, http.StatusBadRequest, apiResponse{
			Error: fmt.Sprintf("caller %q is not a supervised agent; consults need a session to reply into", req.From),
		})
		return
	}

	tk, err := s.consults.Dispatch(cfg, consult.Request{
		From:      req.From,
		Template:  req.Template,
		Model:     req.Model,
		Prompt:    req.Prompt,
		Workspace: workspace,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"id":      tk.ID,
		"harness": tk.Harness,
		"model":   tk.Model,
	}})
}
```

Add imports (`encoding/json`, `net/http`, `github.com/blackpaw-studio/leo/internal/consult`) to the file's import block.

Workspace edge: when the caller has no agent record but is a live config-defined process, `workspace` is `""` — `cmd.Dir = ""` means "inherit the daemon's cwd", which is acceptable for v1 and noted in the doc task. When the template's consultant genuinely needs the caller's repo, the caller is an ephemeral agent and the record carries the workspace.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/web/`
Expected: PASS, including the three new `TestAPIConsult*` tests and all existing web tests.

- [ ] **Step 5: Commit**

```bash
git add internal/web/
git commit -m "feat: POST /api/consult endpoint wiring the consult dispatcher"
```

---

### Task 4: `leo_consult` MCP tool

**Files:**
- Modify: `internal/mcp/client.go` (add `consult` method after `sendMessage`, ~line 153)
- Modify: `internal/mcp/tools.go` (register tool after `leo_send_message`, ~line 231)
- Test: `internal/mcp/server_test.go` (or a new `consult_test.go` in the package, following the `fakeDaemon` pattern at `server_test.go:16`)

**Interfaces:**
- Consumes: `POST /api/consult` contract from Task 3; `daemonClient.do`; `registry.add`, `objectSchema`, `stringArg`, `processName` closure.
- Produces: MCP tool `leo_consult` with args `template` (required), `prompt` (required), `model` (optional).

- [ ] **Step 1: Write the failing test**

Following the existing pattern (`fakeDaemon` + `runRequest` in `server_test.go`), add:

```go
func TestLeoConsultDispatches(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		gotPath = method + " " + path
		json.Unmarshal(body, &gotBody)
		return 200, `{"ok":true,"data":{"id":"c-4f2a","harness":"codex","model":"gpt-5.6-sol"}}`
	})
	defer d.close()

	reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "leo_consult",
			"arguments": map[string]any{"template": "codex", "prompt": "opinion?"},
		},
	})

	if gotPath != "POST /api/consult" {
		t.Fatalf("daemon call %q", gotPath)
	}
	if gotBody["from"] != "assistant" || gotBody["template"] != "codex" || gotBody["prompt"] != "opinion?" {
		t.Fatalf("body %+v", gotBody)
	}
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "c-4f2a") {
		t.Fatalf("tool result %v", content)
	}
}
```

(`fakeDaemon` has `port()` and `close()` methods — `server_test.go:43-48`; the result-content extraction above mirrors `server_test.go:252-259`.)

Also update `TestToolsListContainsCanonicalCommands` (`server_test.go:109`): add `"leo_consult"` to its `want` slice — it enumerates the canonical tool list and will otherwise be a coverage hole (it only checks presence, so it won't fail, but keep it authoritative).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestLeoConsult ./internal/mcp/`
Expected: FAIL — unknown tool `leo_consult`.

- [ ] **Step 3: Implement client method + tool**

`internal/mcp/client.go`, after `sendMessage`:

```go
// consult dispatches a one-off consultant subagent via the daemon. The
// answer is delivered later as an injected message; the returned data
// carries the consult id used in that reply's frame.
func (c *daemonClient) consult(from, template, model, prompt string) (json.RawMessage, error) {
	body := map[string]string{"from": from, "template": template, "prompt": prompt}
	if model != "" {
		body["model"] = model
	}
	return c.do(http.MethodPost, "/api/consult", body)
}
```

`internal/mcp/tools.go`, after the `leo_send_message` registration:

```go
	r.add(toolDef{
		Name: "leo_consult",
		Description: "Dispatch a one-off consultant subagent for a second opinion from another model. " +
			"Pick a template (see leo_list_templates) — it determines the harness and model; `model` optionally overrides the template's model. " +
			"The prompt must be self-contained: the consultant sees none of your conversation, only files in your workspace. " +
			"Returns immediately with a consult id; the answer arrives later as a message framed `[consult <id> · <harness>/<model> · <elapsed>] …`. " +
			"For a council, call this several times with different templates in one turn and reconcile the replies as they arrive.",
		InputSchema: objectSchema(map[string]any{
			"template": map[string]any{"type": "string", "description": "Template name from leo.yaml supplying harness/model/env."},
			"prompt":   map[string]any{"type": "string", "description": "Self-contained question for the consultant."},
			"model":    map[string]any{"type": "string", "description": "Optional model override, validated against the template's harness."},
		}, "template", "prompt"),
	}, func(args map[string]any) (string, error) {
		template, err := stringArg(args, "template")
		if err != nil {
			return "", err
		}
		prompt, err := stringArg(args, "prompt")
		if err != nil {
			return "", err
		}
		model, _ := args["model"].(string)
		data, err := client.consult(processName, template, model, prompt)
		if err != nil {
			return "", err
		}
		var tk struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(data, &tk); err != nil || tk.ID == "" {
			return string(data), nil
		}
		return fmt.Sprintf("Dispatched consult %s to %s (%s/%s). The reply will arrive as a message framed [consult %s · …].",
			tk.ID, template, tk.Harness, tk.Model, tk.ID), nil
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/mcp/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/
git commit -m "feat: leo_consult MCP tool for one-off subagent second opinions"
```

---

### Task 5: docs, full verification, e2e

**Files:**
- Create: `docs/configuration/consults.md`
- Modify: `docs/index.md` (add a link in whatever section lists the configuration docs — mirror how `docs/configuration/persistent-tasks.md` is linked)
- Modify: `CLAUDE.md` (one sentence in the "What is Leo" MCP-adjacent description if a tool list exists; skip if none does)

**Interfaces:**
- Consumes: everything above; no new code.

- [ ] **Step 1: Write `docs/configuration/consults.md`**

```markdown
# Consults — one-off second opinions

Any supervised agent can ask another model for a one-off opinion with the
`leo_consult` MCP tool. Leo runs a headless one-shot subagent on the chosen
template's harness/model in the **caller's workspace** and injects the
answer back into the caller's session as a message.

## Usage (from inside an agent)

- `leo_consult(template: "codex", prompt: "Review this design: …")`
- Optional `model` overrides the template's model (validated against the
  template's harness).
- The tool returns immediately with an id like `c-4f2a`; the reply arrives
  later framed as `[consult c-4f2a · codex/gpt-5.6-sol · 3m12s] …`.
- **Council pattern:** call `leo_consult` several times with different
  templates in one turn, then reconcile the replies as they arrive.

## Semantics and limits

- Templates are the unit of addressing — harness, model, env (including
  third-party endpoints via `env:` maps), and `harness_options` all come
  from the template. There is no consult-specific config.
- The consultant is advisory: a preamble instructs it to analyze without
  modifying files. This is **not enforced** — it runs with the template's
  configured permissions in the caller's workspace.
- One-shot only: no session is kept and no follow-up is possible; spawn a
  real agent for a conversation.
- Timeout 10 minutes; at most 4 consults run concurrently (extra dispatches
  queue). Failures and timeouts are delivered as
  `[consult <id> · … · failed after <elapsed>] <reason>` — never dropped.
- Callers that are config-defined processes without an agent record run the
  consultant in the daemon's working directory (no workspace to inherit).
```

- [ ] **Step 2: Link it from `docs/index.md`**

Open `docs/index.md`, find the configuration-docs list (the entry for `persistent-tasks.md`), and add a matching line for `configuration/consults.md` with the summary "One-off second-opinion subagents via leo_consult".

- [ ] **Step 3: Full local verification**

```bash
go test -race -cover ./...
make lint
make e2e
```

Expected: all pass. `make e2e` is mandatory — this change adds a new argv-building path (PR #97 lesson). If an e2e harness fixture asserts the full MCP tool list, update it to include `leo_consult`.

- [ ] **Step 4: Commit docs**

```bash
git add docs/
git commit -m "docs: consults — one-off second-opinion subagents"
```

- [ ] **Step 5: Branch hygiene + PR**

Work should have been done on a feature branch (e.g. `feat/leo-consult`); if not, create one now and move the commits. Push with `-u`, open a PR titled `feat: leo_consult — one-off subagent second opinions`, summarizing: new `internal/consult` dispatcher, `/api/consult` endpoint, reply injection semantics (readiness-probed, resume-on-suspended), `leo_consult` MCP tool, docs. Include the test plan (unit suites + `make e2e`).
```
