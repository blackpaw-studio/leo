# Agent-to-Agent Messaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every Leo agent/process a built-in `leo_send_message` MCP tool that delivers text into another agent's live Claude prompt, and make all agents automatically aware of it.

**Architecture:** A new MCP tool calls a new daemon route (`POST /web/process/{name}/message`) that injects the message via `tmux send-keys -l <text>` + `Enter`. Ephemeral agents are first given the leo MCP server (currently missing). Awareness is an auto-injected `--append-system-prompt` line, gated on the MCP server being wired in.

**Tech Stack:** Go, cobra, net/http (stdlib mux), tmux, custom stdio JSON-RPC MCP server.

**Spec:** `docs/superpowers/specs/2026-06-02-agent-messaging-design.md`

---

## File Structure

- `internal/leomcp/leomcp.go` — add `MergeSystemPrompt(cfg, userPrompt) string` + the awareness text constant. (helper, leaf)
- `internal/agent/args.go` — wire `leomcp.AppendArg` + `MergeSystemPrompt` into ephemeral-agent args.
- `internal/cli/service.go` — use `MergeSystemPrompt` at `buildProcessArgs`.
- `internal/run/runner.go` — use `MergeSystemPrompt` at `buildArgs`.
- `internal/web/handlers.go` — new `handleProcessMessage` handler (literal send + Enter, validates target via `States()`).
- `internal/web/web.go` — register `POST /web/process/{name}/message`.
- `internal/mcp/client.go` — `sendMessage(target, text) error` daemon client method.
- `internal/mcp/tools.go` — `leo_send_message` tool definition + handler.
- Matching `_test.go` files in each package.

---

## Task 1: `leomcp.MergeSystemPrompt` helper

**Files:**
- Modify: `internal/leomcp/leomcp.go`
- Test: `internal/leomcp/leomcp_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/leomcp/leomcp_test.go`:

```go
func TestMergeSystemPrompt(t *testing.T) {
	enabled := &config.Config{Web: config.WebConfig{Enabled: true}}
	disabled := &config.Config{Web: config.WebConfig{Enabled: false}}

	tests := []struct {
		name       string
		cfg        *config.Config
		user       string
		wantHas    []string // substrings that must appear
		wantEmpty  bool
	}{
		{"disabled+no user", disabled, "", nil, true},
		{"disabled keeps user only", disabled, "be terse", []string{"be terse"}, false},
		{"enabled adds awareness", enabled, "", []string{"leo_send_message"}, false},
		{"enabled merges both", enabled, "be terse", []string{"leo_send_message", "be terse"}, false},
		{"nil cfg keeps user", nil, "be terse", []string{"be terse"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeSystemPrompt(tt.cfg, tt.user)
			if tt.wantEmpty && got != "" {
				t.Fatalf("want empty, got %q", got)
			}
			for _, sub := range tt.wantHas {
				if !strings.Contains(got, sub) {
					t.Errorf("result missing %q; got %q", sub, got)
				}
			}
			// When enabled, the awareness text precedes the user text.
			if tt.cfg != nil && tt.cfg.Web.Enabled && tt.user != "" {
				if strings.Index(got, "leo_send_message") > strings.Index(got, tt.user) {
					t.Errorf("awareness text should come before user text; got %q", got)
				}
			}
		})
	}
}
```

Add `"strings"` to the test file's imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/leomcp/ -run TestMergeSystemPrompt`
Expected: FAIL — `undefined: MergeSystemPrompt`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/leomcp/leomcp.go` (the file already imports `config`):

```go
// messagingAwareness is the built-in system-prompt line that tells a Claude
// process it can message other Leo agents. Injected only when the leo MCP
// server is wired in (see MergeSystemPrompt).
const messagingAwareness = "You can send a message to another Leo agent or process with the `leo_send_message` tool — set `to` to its name and `message` to the text. Use `leo_list_agents` to see which agents are running. The message arrives in the recipient's prompt as a new turn."

// MergeSystemPrompt combines Leo's built-in append-system-prompt additions
// with any user-configured prompt into a single value. The built-in
// messaging-awareness line is included only when the leo MCP server is wired
// in (cfg.Web.Enabled) — the same gate AppendArg uses — so an agent is told
// it can message others exactly when it actually can. Returns "" when there
// is nothing to append.
func MergeSystemPrompt(cfg *config.Config, userPrompt string) string {
	var builtin string
	if cfg != nil && cfg.Web.Enabled {
		builtin = messagingAwareness
	}
	switch {
	case builtin == "":
		return userPrompt
	case userPrompt == "":
		return builtin
	default:
		return builtin + "\n\n" + userPrompt
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/leomcp/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/leomcp/leomcp.go internal/leomcp/leomcp_test.go
git commit -m "feat(leomcp): MergeSystemPrompt helper for built-in awareness line"
```

---

## Task 2: Wire leo MCP server + awareness into ephemeral agents

`BuildTemplateArgs` does not currently call `leomcp.AppendArg`, so ephemeral agents get no `leo_*` tools at all. Add the MCP config and route the append-system-prompt through `MergeSystemPrompt`.

**Files:**
- Modify: `internal/agent/args.go`
- Test: `internal/agent/args_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/agent/args_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// hasFlagValue reports whether args contains `flag` immediately followed by a
// value that contains `substr`.
func hasFlagValue(args []string, flag, substr string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && strings.Contains(args[i+1], substr) {
			return true
		}
	}
	return false
}

func TestBuildTemplateArgsWiresLeoMCPWhenWebEnabled(t *testing.T) {
	// HomePath must be set: AppendArg writes leo-mcp.json under StatePath
	// (HomePath/state). An empty HomePath would write relative to cwd.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("expected awareness line in --append-system-prompt; got %v", args)
	}
}

func TestBuildTemplateArgsNoLeoMCPWhenWebDisabled(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: false}}
	args := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "")

	if hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("awareness line must not appear when web disabled; got %v", args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuildTemplateArgs`
Expected: FAIL — no `--mcp-config` / no awareness line.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/args.go`, add the import:

```go
	"github.com/blackpaw-studio/leo/internal/leomcp"
```

Replace the existing append-system-prompt block:

```go
	appendPrompt := tmpl.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}
```

with:

```go
	appendPrompt := tmpl.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	appendPrompt = leomcp.MergeSystemPrompt(cfg, appendPrompt)
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}
```

Then, immediately before the `maxTurns` block (after the disallowed-tools block), layer in the leo MCP server so agents get the `leo_*` tools:

```go
	// Layer in the Leo-managed MCP server (when the daemon's TCP listener is
	// enabled) so ephemeral agents get the universal leo_* tools — including
	// leo_send_message for agent-to-agent messaging.
	args = leomcp.AppendArg(args, cfg)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/agent/ -run TestBuildTemplateArgs`
Expected: PASS. (Note: `AppendArg` writes the MCP config under `cfg.StatePath()`; with a bare `config.Config{}` that resolves under the user's leo home — acceptable for the test, which only inspects the flag, not file contents.)

- [ ] **Step 5: Run the full agent package tests**

Run: `go test -race ./internal/agent/`
Expected: PASS (existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/args.go internal/agent/args_test.go
git commit -m "feat(agent): wire leo MCP server + messaging awareness into ephemeral agents"
```

---

## Task 3: Apply MergeSystemPrompt at the process and task arg sites

**Files:**
- Modify: `internal/cli/service.go` (`buildProcessArgs`, ~line 337)
- Modify: `internal/run/runner.go` (`buildArgs`, ~line 403)
- Test: `internal/cli/service_test.go`, `internal/run/runner_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/cli/service_test.go`:

```go
func TestBuildProcessArgsInjectsMessagingAwareness(t *testing.T) {
	// HomePath set so AppendArg's EnsureConfig writes under a temp dir.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	args := buildProcessArgs(cfg, "assistant", config.ProcessConfig{})

	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--append-system-prompt" && strings.Contains(args[i+1], "leo_send_message") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected messaging awareness in process args; got %v", args)
	}
}
```

Add to `internal/run/runner_test.go`:

```go
func TestBuildArgsInjectsMessagingAwareness(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	args := buildArgs(cfg, config.TaskConfig{}, "do the thing", "sess-1")

	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--append-system-prompt" && strings.Contains(args[i+1], "leo_send_message") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected messaging awareness in task args; got %v", args)
	}
}
```

Ensure both test files import `"strings"` and `"github.com/blackpaw-studio/leo/internal/config"` (add if missing).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run TestBuildProcessArgsInjectsMessagingAwareness && go test ./internal/run/ -run TestBuildArgsInjectsMessagingAwareness`
Expected: FAIL — awareness line absent.

- [ ] **Step 3: Implement in `service.go`**

In `internal/cli/service.go` `buildProcessArgs`, replace:

```go
	appendPrompt := proc.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		claudeArgs = append(claudeArgs, "--append-system-prompt", appendPrompt)
	}
```

with:

```go
	appendPrompt := proc.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	appendPrompt = leomcp.MergeSystemPrompt(cfg, appendPrompt)
	if appendPrompt != "" {
		claudeArgs = append(claudeArgs, "--append-system-prompt", appendPrompt)
	}
```

(`leomcp` is already imported in this file.)

- [ ] **Step 4: Implement in `runner.go`**

In `internal/run/runner.go` `buildArgs`, replace:

```go
	appendPrompt := task.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}
```

with:

```go
	appendPrompt := task.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	appendPrompt = leomcp.MergeSystemPrompt(cfg, appendPrompt)
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}
```

(`leomcp` is already imported in this file.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/cli/ -run TestBuildProcessArgs && go test -race ./internal/run/ -run TestBuildArgs`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/service.go internal/cli/service_test.go internal/run/runner.go internal/run/runner_test.go
git commit -m "feat(cli,run): inject messaging awareness into process and task prompts"
```

---

## Task 4: Daemon route `POST /web/process/{name}/message`

Delivers a literal message into the target session and submits it. Validates the target against the supervisor's running sessions (`States()`), so the not-found error doubles as the recipient list.

**Files:**
- Modify: `internal/web/handlers.go` (add `handleProcessMessage`, near `handleProcessSendKeys` ~line 779)
- Modify: `internal/web/web.go` (register route near line 175)
- Test: `internal/web/web_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/web/web_test.go`:

```go
func TestProcessMessageSendsLiteralThenEnter(t *testing.T) {
	s, _ := newTestServer(t)

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("true") // harmless no-op
	}

	body := strings.NewReader(`{"text":"Enter the build status please"}`)
	req := httptest.NewRequest("POST", "/web/process/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// Expect two tmux invocations: literal send (-l) then Enter.
	if len(calls) != 2 {
		t.Fatalf("expected 2 tmux calls, got %d: %v", len(calls), calls)
	}
	first := strings.Join(calls[0], " ")
	if !strings.Contains(first, "send-keys") || !strings.Contains(first, "-l") ||
		!strings.Contains(first, "leo-assistant") || !strings.Contains(first, "Enter the build status please") {
		t.Errorf("first call should be literal send to leo-assistant; got %v", calls[0])
	}
	last := calls[1]
	if last[len(last)-1] != "Enter" {
		t.Errorf("second call should submit with Enter; got %v", last)
	}
}

func TestProcessMessageUnknownTargetListsRecipients(t *testing.T) {
	s, _ := newTestServer(t) // mock has process "assistant"

	body := strings.NewReader(`{"text":"hi"}`)
	req := httptest.NewRequest("POST", "/web/process/ghost/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "assistant") {
		t.Errorf("not-found error should list recipients; got %s", w.Body.String())
	}
}

func TestProcessMessageRejectsEmptyText(t *testing.T) {
	s, _ := newTestServer(t)
	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest("POST", "/web/process/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}
```

Ensure `internal/web/web_test.go` imports `"os/exec"` (alias-free `exec`) and `"strings"` (both likely present; add if not).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestProcessMessage`
Expected: FAIL — route not registered (404 for the literal-send test too / no calls recorded).

- [ ] **Step 3: Register the route**

In `internal/web/web.go`, directly after the existing send-keys registration (line ~175):

```go
	mux.HandleFunc("POST /web/process/{name}/send", s.handleProcessSendKeys)
	mux.HandleFunc("POST /web/process/{name}/message", s.handleProcessMessage)
```

- [ ] **Step 4: Implement the handler**

In `internal/web/handlers.go`, add after `handleProcessSendKeys`:

```go
// handleProcessMessage delivers a free-text message into a process/agent's
// live Claude prompt and submits it. Unlike handleProcessSendKeys (which types
// char-by-char to drive slash-command menus), this sends the body verbatim
// with `send-keys -l` so arbitrary text — including tmux key names like
// "Enter" or "C-c" — is typed literally, then submits with a separate Enter.
//
// POST /web/process/{name}/message  {"text": "hello"}
func (s *Server) handleProcessMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "text is required"})
		return
	}

	// Validate the target against running sessions (processes + agents).
	states := s.processes.States()
	if _, ok := states[name]; !ok {
		names := make([]string, 0, len(states))
		for n := range states {
			names = append(names, n)
		}
		sort.Strings(names)
		writeJSON(w, http.StatusNotFound, apiResponse{
			Error: fmt.Sprintf("no such agent or process %q; running: %s", name, strings.Join(names, ", ")),
		})
		return
	}

	sessionName := "leo-" + name
	tmuxPath := findTmuxPath()

	// Literal paste of the message body, then a separate Enter to submit.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", sessionName, "-l", req.Text)...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send message failed: %v", err)})
		return
	}
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", sessionName, "Enter")...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("submit message failed: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}
```

Ensure `internal/web/handlers.go` imports `"sort"` (add to the import block if missing; `json`, `fmt`, `strings`, and the `tmux` package are already imported).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/web/ -run TestProcessMessage`
Expected: PASS (all three sub-tests).

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers.go internal/web/web.go internal/web/web_test.go
git commit -m "feat(web): /web/process/{name}/message route for literal message delivery"
```

---

## Task 5: MCP daemon client `sendMessage`

**Files:**
- Modify: `internal/mcp/client.go`
- Test: `internal/mcp/client_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/mcp/client_test.go`:

```go
func TestDaemonClientSendMessage(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{}}`))
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	c := newDaemonClient(port, "")
	if err := c.sendMessage("worker-1", "build is green"); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if gotPath != "/web/process/worker-1/message" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, "build is green") {
		t.Errorf("body = %q", gotBody)
	}
}
```

Add `"io"` to the test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/ -run TestDaemonClientSendMessage`
Expected: FAIL — `c.sendMessage undefined`.

- [ ] **Step 3: Implement**

Add to `internal/mcp/client.go`, alongside the other client methods:

```go
func (c *daemonClient) sendMessage(target, text string) error {
	_, err := c.do(http.MethodPost, "/web/process/"+target+"/message", map[string]any{"text": text})
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/mcp/ -run TestDaemonClientSendMessage`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/client.go internal/mcp/client_test.go
git commit -m "feat(mcp): sendMessage daemon client method"
```

---

## Task 6: `leo_send_message` MCP tool

**Files:**
- Modify: `internal/mcp/tools.go`
- Test: `internal/mcp/server_test.go`

- [ ] **Step 1: Write the failing tests**

First, extend the existing canonical-tools assertion in `internal/mcp/server_test.go`:

```go
	want := []string{
		"leo_clear", "leo_compact", "leo_interrupt",
		"leo_list_tasks", "leo_run_task", "leo_toggle_task",
		"leo_list_templates", "leo_spawn_agent", "leo_list_agents", "leo_stop_agent",
		"leo_send_message",
	}
```

Then add new tests to `internal/mcp/server_test.go`:

```go
func TestSendMessageDeliversWithSenderPrefix(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{}}`))
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	reg := newRegistry(newDaemonClient(port, ""), "sender-proc")
	out, err := reg.call("leo_send_message", json.RawMessage(`{"to":"worker-1","message":"ping"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(gotBody, "sender-proc") || !strings.Contains(gotBody, "ping") {
		t.Errorf("delivered body should carry sender + message; got %q", gotBody)
	}
	if !strings.Contains(out, "worker-1") {
		t.Errorf("result should confirm target; got %q", out)
	}
}

func TestSendMessageRejectsSelf(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "self-proc")
	_, err := reg.call("leo_send_message", json.RawMessage(`{"to":"self-proc","message":"hi"}`))
	if err == nil {
		t.Fatal("expected error when messaging self")
	}
	if !strings.Contains(err.Error(), "self-proc") {
		t.Errorf("error should mention the self name; got %v", err)
	}
}

func TestSendMessageRequiresMessage(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "self-proc")
	_, err := reg.call("leo_send_message", json.RawMessage(`{"to":"worker-1"}`))
	if err == nil {
		t.Fatal("expected error when message missing")
	}
}
```

Ensure `internal/mcp/server_test.go` imports `"io"`, `"net/http"`, `"net/http/httptest"`, `"strings"`, `"encoding/json"` (add any missing).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcp/ -run 'TestSendMessage|TestToolsListContainsCanonicalCommands'`
Expected: FAIL — unknown tool `leo_send_message`.

- [ ] **Step 3: Implement the tool**

In `internal/mcp/tools.go`, inside `newRegistry`, add before `return r` (after `leo_stop_agent`):

```go
	r.add(toolDef{
		Name:        "leo_send_message",
		Description: "Send a text message to another Leo agent or process. It arrives in the recipient's Claude prompt as a new turn, prefixed with your name. Use leo_list_agents to discover running agents. 'to' is the target's name; 'message' is the text.",
		InputSchema: objectSchema(map[string]any{
			"to":      map[string]any{"type": "string", "description": "Target agent/process name (as shown by leo_list_agents or leo status)."},
			"message": map[string]any{"type": "string", "description": "The message body to deliver."},
		}, "to", "message"),
	}, func(args map[string]any) (string, error) {
		to, err := stringArg(args, "to")
		if err != nil {
			return "", err
		}
		message, err := stringArg(args, "message")
		if err != nil {
			return "", err
		}
		if to == processName {
			return "", fmt.Errorf("cannot send a message to yourself (%q)", processName)
		}
		body := fmt.Sprintf("[message from %s] %s", processName, message)
		if err := client.sendMessage(to, body); err != nil {
			return "", err
		}
		return "Sent message to " + to, nil
	})
```

(`fmt` is already imported in `tools.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/mcp/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/server_test.go
git commit -m "feat(mcp): leo_send_message tool for agent-to-agent messaging"
```

---

## Task 7: Full verification

- [ ] **Step 1: Build**

Run: `make build`
Expected: builds `bin/leo` with no errors.

- [ ] **Step 2: Full test suite with race + coverage**

Run: `make test`
Expected: all packages PASS; no race warnings.

- [ ] **Step 3: Lint**

Run: `make lint`
Expected: `go vet` + `staticcheck` clean. (If gosec runs in CI, note that the new handler builds tmux args from a validated path constant + request body passed as a single literal arg — no shell, no command injection surface.)

- [ ] **Step 4: Commit any lint fixups (if needed)**

```bash
git add -A
git commit -m "chore: lint/format fixups for agent messaging"
```

---

## Self-Review Notes (spec coverage)

- Spec §1 (enable MCP for agents) → Task 2.
- Spec §2 (`leo_send_message` tool, sender prefix, self/empty guards) → Task 6.
- Spec §3 (literal send + Enter via new route) → Task 4.
- Spec §4 (validate against running sessions, error lists recipients) → Task 4 (`States()`-based).
- Spec §5 (automatic awareness via merged append-system-prompt) → Tasks 1, 2, 3 + tool description in Task 6.
- Spec testing section → per-task tests + Task 7 full run.
- Out-of-scope items: none implemented (no inbox, receipts, broadcast).
