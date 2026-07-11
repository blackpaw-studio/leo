# Harness Plan 3: Codex + Opencode Adapters — One-Shot Tasks

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `codex` and `opencode` adapters for scheduled one-shot tasks (`KindTask` only), extend the `Harness` interface with `ParseEvents`/`Env`/`SupportsKind`, and thread per-harness binary + parsing + env through the task runner.

**Architecture:** Two new adapter packages (`internal/harness/codex`, `internal/harness/opencode`) implement the extended interface. The runner (`internal/run`) stops hardcoding claude: it resolves the task's harness once, checks the binary exists, renders argv via `Args()`, merges `Env()` into the spawn env, and parses output via `ParseEvents()`. Non-task kinds on the new harnesses fail loudly at `Args()` and at config validation. The leo MCP server is the messaging bridge on both new harnesses (channels stay claude-only).

**Tech Stack:** Go, stdlib only. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-10-harness-abstraction-design.md` (Plan 3 implements its codex/opencode one-shot scope)

## Verified CLI facts (2026-07-10, empirical + docs)

All captured streams live in `~/leo-plan3-fixtures/` on this machine (cx-\* = codex 0.144.1, oc-\* = opencode 1.17.7). **Do not re-derive these; they were verified against live binaries.**

**codex (0.144.1):**
- One-shot: `codex exec --json <flags> [resume <id>] <prompt>`. Parent flags come **before** the `resume` subcommand; prompt is positional last. Verified argv: `exec --json --skip-git-repo-check resume <thread-id> <prompt>`.
- `--json` emits JSONL: `thread.started` (field `thread_id`), `turn.started`, `item.started`/`item.completed` (item types incl. `agent_message` with `text`, `mcp_tool_call`, `error` with `message`), `turn.completed` (usage), top-level `error` (field `message`), `turn.failed` (field `error.message`). Exit 1 on failure, 0 on success.
- Stale resume id: exit 1, **empty stdout**, stderr `Error: thread/resume: thread/resume failed: no rollout found for thread id <id> (code -32600)`.
- Non-git workspace without `--skip-git-repo-check`: exit 1, `Not inside a trusted directory and --skip-git-repo-check was not specified.` Leo workspaces are often not git repos → the adapter **always** passes `--skip-git-repo-check`.
- `codex exec` has **no approval flag** — headless approval policy is hardcoded `never` (upstream removed `-a` from exec; `on-failure` is deprecated everywhere). `--sandbox` values: `read-only`, `workspace-write`, `danger-full-access`.
- MCP bridge (all verified live against the running leo daemon): `-c mcp_servers.leo.command=leo`, `-c mcp_servers.leo.args=["mcp-server"]`, `-c mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN"]` (parent-env whitelist — keeps the token out of ps-visible argv values), plus `-c mcp_servers.leo.default_tools_approval_mode="approve"`. **Without the approval-mode key, MCP tool calls are auto-cancelled** (`user cancelled MCP tool call`) under any sandbox except `danger-full-access`. With it, calls complete under the default read-only sandbox. The key is scoped to the `leo` server only.
- `CODEX_API_KEY` honored by `exec` directly (no config gate needed); ChatGPT login state also works. Auth is the user's concern (task `env:` or ambient login).
- Model: free-form string, validated server-side only.
- codex reads stdin ("Reading additional input from stdin..."); Go's default `/dev/null` child stdin is safe (issue #20919 only bites on open-pipe stdin).

**opencode (1.17.7; latest 1.17.18):**
- One-shot: `opencode run --format json [--model provider/model] [-s <session-id>] <prompt>`. JSONL events: `step_start`, `text` (text in `part.text`), `tool_use`, `step_finish`, `error` (shape `{"type":"error","sessionID":"...","error":{"name":"...","data":{"message":"..."}}}`). Every event carries top-level `sessionID` (`ses_…`).
- Upstream bug #26855 (final `step_finish` not flushed) is **fixed** in v1.16+ but ParseEvents must still treat **EOF as turn end** and never require `step_finish` (older versions, `--attach` mode excluded from the fix).
- Bad model: exit 1, stdout contains a **multi-line non-JSON log blob** before the JSON `error` events → the parser must skip unparseable lines.
- Stale session (`-s` unknown): exit 1, **empty stdout**, stderr `Error: Session not found` (with ANSI codes).
- Exit codes are historically unreliable (multiple exit-0-on-error upstream bugs) → the runner must also honor `Result.IsError`.
- `OPENCODE_CONFIG_CONTENT` env var: inline JSON config, highest-priority overlay, deep-merged over the user's config. Verified live: `{"permission":{"bash":"deny"}}` blocked bash in `run`. MCP block shape: `{"mcp":{"leo":{"type":"local","command":["leo","mcp-server"],"enabled":true,"environment":{...}}}}`.
- Permissions: config-only map, values `allow|ask|deny` (string) or pattern maps (e.g. `{"bash":{"git *":"allow","*":"ask"}}`).
- Model **must** be `provider/model`.
- No append-system-prompt equivalent on either CLI (codex has a `-c developer_instructions=...` config key — deliberately **not** used in this plan; noted in docs as future work).

## Global Constraints

- **Claude behavior stays byte-identical** except two deliberate, tested changes called out in Task 5: (a) `CLAUDE_CODE_ENTRYPOINT=cli` moves from `executeCommand` into the claude adapter's `Env()` (same value ends up in the child env — assert this), and (b) a zero-exit attempt whose parsed `Result.IsError` is true now counts as a failed attempt (defensive; claude exits nonzero on real errors, opencode sometimes does not).
- **Characterization polarity:** when a task says "characterization test", the test must PASS against the code as it exists before that task's rewire. New functionality uses normal TDD (RED → GREEN).
- Error text follows existing conventions: `ValidateModel` errors phrase as `%q is not valid (…)` with no leading field path; `DecodeOptions` unknown-key errors as `unknown option %q (valid: …)`.
- Every commit: `go test -race ./...` green, `make lint` clean, **`make e2e` green** (the e2e suite is build-tagged — `go test ./...` skips it; this bit PR #97). Changed packages hold ≥80% coverage.
- Commit format: `<type>: <description>` (feat/fix/refactor/test/docs/chore). No attribution lines.
- ParseEvents fixtures are the **captured real streams** pre-seeded at `internal/harness/codex/testdata/` and `internal/harness/opencode/testdata/` (Task 0). Do not hand-invent stream shapes.
- No mutation of shared config files at spawn time: codex config goes via `-c` argv, opencode via `OPENCODE_CONFIG_CONTENT` env. Never write to `~/.codex/config.toml` or `opencode.json`.

## Task Ordering

Strictly sequential: 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7. (Adapters before config so blank imports resolve; config before runner so validation invariants hold; e2e after runner.)

## Not In This Plan (later plans)

- Session drivers: codex TurnDriver / opencode ServerDriver for processes, ephemeral agents, persistent sessions (Plan 4). Non-task kinds on the new harnesses **must fail loudly** — that's in scope here.
- Web UI `OptionsSchema()` forms and harness dropdown (Plan 5). The web schema registry already excludes `harness`/`harness_options` from generic forms.
- Builtin messaging-awareness system prompt on codex/opencode (codex `developer_instructions` exists; deferred — MCP tools are self-describing).
- Migrating Evan's live `~/.leo/leo.yaml` (separate follow-up, gated on his explicit go).

---

### Task 0: Pre-seed captured fixtures (orchestrator, no subagent)

**Files:**
- Create: `internal/harness/codex/testdata/fresh.jsonl` (from `~/leo-plan3-fixtures/cx-fresh.jsonl`)
- Create: `internal/harness/codex/testdata/resume.jsonl` (cx-resume.jsonl)
- Create: `internal/harness/codex/testdata/badmodel.jsonl` (cx-badmodel.jsonl)
- Create: `internal/harness/codex/testdata/mcp_tool_call.jsonl` (cx-mcp5.jsonl **sanitized**: replace the `mcp_tool_call` items' `result.content[0].text` agent inventory with a 2-entry fake list; keep event/item structure byte-faithful)
- Create: `internal/harness/codex/testdata/mcp_cancelled.jsonl` (cx-mcp.jsonl verbatim — two failed `mcp_tool_call` items with `error.message` "user cancelled MCP tool call", `result` null, so no sanitization needed)
- Create: `internal/harness/opencode/testdata/fresh.jsonl` (oc-stdout.jsonl)
- Create: `internal/harness/opencode/testdata/resume.jsonl` (oc-resume.jsonl)
- Create: `internal/harness/opencode/testdata/badmodel.jsonl` (oc-badmodel.jsonl — includes the real non-JSON log blob)
- Create: `internal/harness/opencode/testdata/multistep_deny.jsonl` (oc-deny.jsonl — two steps, tool_use, final text)
- Create: `internal/harness/opencode/testdata/truncated_no_step_finish.jsonl` (oc-stdout.jsonl minus its final `step_finish` line — the #26855 shape)
- Create: `internal/harness/codex/testdata/README.md` + `internal/harness/opencode/testdata/README.md` (one paragraph: captured 2026-07-10 from codex 0.144.1 / opencode 1.17.7, which file shows what, sanitization note for mcp_tool_call.jsonl)

- [ ] **Step 1:** Copy + sanitize as above. `git add` + commit directly on the feature branch:

```bash
git add internal/harness/codex/testdata internal/harness/opencode/testdata
git commit -m "test(harness): captured codex/opencode stream fixtures"
```

---

### Task 1: Interface v3 — Result, ParseEvents, Env, SupportsKind + claude implementations

**Files:**
- Modify: `internal/harness/harness.go`
- Modify: `internal/harness/registry_test.go` (fake harness gains the new methods)
- Create: `internal/harness/claude/parse.go`
- Create: `internal/harness/claude/parse_test.go`
- Modify: `internal/harness/claude/claude.go` (Env, SupportsKind)
- Modify: `internal/harness/claude/claude_test.go`
- Modify: `internal/run/runner.go` + `internal/run/runner_test.go` (ONLY the mechanical move of `parseClaudeOutput`/`claudeResult`/`streamEvent` into the adapter — the full runner rewire is Task 5)

**Interfaces:**
- Produces: `harness.Result{SessionID, Text string; IsError bool; Errors []string}`; `Harness.ParseEvents(r io.Reader) (Result, error)`; `Harness.Env(spec LaunchSpec) (map[string]string, error)`; `Harness.SupportsKind(k Kind) bool`. Claude: `ParseEvents` reproduces `parseClaudeOutput` semantics; `Env` returns `{"CLAUDE_CODE_ENTRYPOINT":"cli"}` for `KindTask`, nil otherwise; `SupportsKind` true for all kinds.
- Consumed by: Tasks 2–5.

- [ ] **Step 1: Extend the interface** in `internal/harness/harness.go` — add after the `LaunchSpec` definition:

```go
// Result is the parsed outcome of a one-shot run's output stream.
type Result struct {
	SessionID string   // harness-native session/thread ID; empty if none seen
	Text      string   // final result text
	IsError   bool     // the stream carried a fatal error event/flag
	Errors    []string // error messages accumulated from the stream
}
```

and add to the `Harness` interface:

```go
	// ParseEvents parses the harness's one-shot output stream (stdout, or
	// combined stdout+stderr) into a Result. Unparseable lines are skipped,
	// never fatal; EOF is end-of-turn. The error return is reserved for
	// reader failures, not stream content.
	ParseEvents(r io.Reader) (Result, error)

	// Env returns harness-specific extra process env for a launch (merged
	// into the spawn env by the caller; caller-provided env must win on
	// collision). Nil when the harness needs nothing.
	Env(spec LaunchSpec) (map[string]string, error)

	// SupportsKind reports whether this harness can run the given leo
	// primitive. Kinds outside this set must also fail loudly in Args().
	SupportsKind(k Kind) bool
```

- [ ] **Step 2: Update `registry_test.go`** — the in-test fake harness implements the three new methods (`Result{}, nil` / `nil, nil` / `true`).

- [ ] **Step 3: Run to verify compile FAIL**: `go test -race ./internal/harness/...` — expected: `claude.Claude` does not implement `harness.Harness` (missing ParseEvents).

- [ ] **Step 4: Characterization + new tests for claude.** Create `internal/harness/claude/parse_test.go`. The ParseEvents cases are ported verbatim from the existing `parseClaudeOutput` coverage in `internal/run/runner_test.go` (grep `parseClaudeOutput` there and port every case), re-expressed against the new API, plus:

```go
func TestParseEventsStreamJSON(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}
{"type":"result","session_id":"abc-123","result":"done","is_error":false}
`
	res, err := Claude{}.ParseEvents(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{SessionID: "abc-123", Text: "done"}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
}

func TestParseEventsSingleObjectFallback(t *testing.T) {
	res, _ := Claude{}.ParseEvents(strings.NewReader(`{"session_id":"s1","result":"ok"}`))
	if res.SessionID != "s1" || res.Text != "ok" {
		t.Errorf("fallback parse got %+v", res)
	}
}

func TestParseEventsErrors(t *testing.T) {
	stream := `{"type":"result","session_id":"s2","is_error":true,"errors":["boom"]}`
	res, _ := Claude{}.ParseEvents(strings.NewReader(stream))
	if !res.IsError || len(res.Errors) != 1 || res.Errors[0] != "boom" {
		t.Errorf("got %+v", res)
	}
}
```

In `claude_test.go` add table-driven `TestClaudeEnv` (KindTask → `{"CLAUDE_CODE_ENTRYPOINT":"cli"}`; KindProcess/KindAgent/KindSession → nil) and `TestClaudeSupportsKind` (all four true).

Run: `go test -race ./internal/harness/claude/` — expected: FAIL (undefined).

- [ ] **Step 5: Implement.** Create `internal/harness/claude/parse.go` — move `claudeResult`, `streamEvent`, and the `parseClaudeOutput` body from `internal/run/runner.go` (delete them there; Task 1 leaves a thin shim in the runner so it still compiles — see Step 6):

```go
package claude

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// claudeResult is the minimal structure for parsing the final "result" event
// from claude --output-format stream-json (newline-delimited JSON).
type claudeResult struct {
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	IsError   bool     `json:"is_error"`
	Errors    []string `json:"errors"`
}

// streamEvent represents a single event line from stream-json output.
type streamEvent struct {
	Type string `json:"type"`
	claudeResult
}

// ParseEvents extracts the final result from stream-json (NDJSON) output.
// It scans for the last line with "type":"result"; falls back to parsing the
// whole payload as a single JSON object (old --output-format json).
func (Claude) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var best claudeResult
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt streamEvent
		if json.Unmarshal(line, &evt) == nil && evt.Type == "result" {
			best = evt.claudeResult
		}
	}
	if best.SessionID == "" && best.Result == "" && len(best.Errors) == 0 {
		// Fallback: single JSON object (old --output-format json).
		_ = json.Unmarshal(output, &best)
	}
	return harness.Result{
		SessionID: best.SessionID,
		Text:      best.Result,
		IsError:   best.IsError,
		Errors:    best.Errors,
	}, nil
}
```

In `claude.go` add:

```go
// Env returns claude-specific spawn env. One-shot task runs set the CLI
// entrypoint marker (moved here from the task runner); interactive kinds
// export their env at tmux launch instead.
func (Claude) Env(spec harness.LaunchSpec) (map[string]string, error) {
	if spec.Kind == harness.KindTask {
		return map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, nil
	}
	return nil, nil
}

// SupportsKind: claude runs every leo primitive.
func (Claude) SupportsKind(harness.Kind) bool { return true }
```

- [ ] **Step 6: Shim the runner (mechanical).** In `internal/run/runner.go` delete `claudeResult`, `streamEvent`, and `parseClaudeOutput`; replace every `parseClaudeOutput(output)` call and every `claudeResult` mention with:

```go
// parseClaudeOutput is a Task-5 transition shim; the full per-harness
// threading replaces it.
func parseClaudeOutput(output []byte) harness.Result {
	res, _ := claudeharness.Claude{}.ParseEvents(bytes.NewReader(output))
	return res
}
```

`attemptResult.result` becomes `harness.Result`; `isSessionError` reads `result.Text` where it read `result.Result` (field rename only — logic untouched). Update `runner_test.go` references mechanically (the ported parse tests were moved in Step 4; delete the originals here).

- [ ] **Step 7: Verify + commit**

```bash
go test -race -cover ./... && make lint && make e2e
git add internal/harness/ internal/run/
git commit -m "feat(harness): ParseEvents, Env, SupportsKind on the interface; claude stream parsing moves into the adapter"
```

---

### Task 2: Codex adapter

**Files:**
- Create: `internal/harness/codex/codex.go`
- Create: `internal/harness/codex/options.go`
- Create: `internal/harness/codex/parse.go`
- Create: `internal/harness/codex/codex_test.go`, `options_test.go`, `parse_test.go`
- (testdata pre-seeded by Task 0)

**Interfaces:**
- Consumes: `harness.Register`, `harness.LaunchSpec`, `harness.Result`.
- Produces: `codex.Codex{}` registered as `"codex"`; `codex.Options{Sandbox string; LeoMCP *LeoMCPBridge}`; `codex.LeoMCPBridge{Command string; Args, EnvVars []string; ApprovalMode string}`. Task 5 constructs `LeoMCPBridge` and type-switches on `codex.Options`.

- [ ] **Step 1: Write failing tests.** `options_test.go` (table-driven):
  - `{"sandbox":"read-only"}`, `"workspace-write"`, `"danger-full-access"` decode; any other sandbox value errors: `sandbox %q is not valid (use read-only, workspace-write, or danger-full-access)`.
  - `sandbox: 5` errors `option "sandbox" must be a string, got int`.
  - `{"approval":"never"}` errors: `option "approval" is not supported: codex exec always runs non-interactively (approval policy "never")`.
  - `{"append_system_prompt":"x"}` errors: `option "append_system_prompt" is not supported: codex has no append-system-prompt equivalent (use the workspace AGENTS.md)`.
  - unknown key errors `unknown option "foo" (valid: sandbox)`; nil/empty map → zero Options; two bad keys fail on the lexicographically first.

  `codex_test.go`:
  - `TestValidateModel`: `""` ok, `"gpt-5.3-codex"` ok, `"o4-mini"` ok; `"gpt 5"` (whitespace) errors `%q is not valid (must not contain whitespace)`.
  - `TestSupportsChannels` → false. `TestSupportsKind`: KindTask true; process/agent/session false.
  - `TestSessionArgs`: resume → `["resume","tid-1"]`; none → nil; pinned → nil.
  - `TestArgs` golden table (exact argv, in this flag order):

```go
{
	name: "fresh minimal",
	spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "do it", Options: Options{}},
	want: []string{"exec", "--json", "--skip-git-repo-check", "do it"},
},
{
	name: "model, sandbox, resume",
	spec: harness.LaunchSpec{
		Kind: harness.KindTask, Prompt: "again", Model: "gpt-5.3-codex",
		Session: harness.SessionState{Mode: harness.SessionResume, ID: "tid-9"},
		Options: Options{Sandbox: "workspace-write"},
	},
	want: []string{"exec", "--json", "--skip-git-repo-check",
		"--model", "gpt-5.3-codex", "--sandbox", "workspace-write",
		"resume", "tid-9", "again"},
},
{
	name: "leo MCP bridge",
	spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", Options: Options{
		LeoMCP: &LeoMCPBridge{
			Command: "leo", Args: []string{"mcp-server"},
			EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
			ApprovalMode: "approve",
		},
	}},
	want: []string{"exec", "--json", "--skip-git-repo-check",
		"-c", `mcp_servers.leo.command="leo"`,
		"-c", `mcp_servers.leo.args=["mcp-server"]`,
		"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN"]`,
		"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`,
		"p"},
},
```

  Error cases: KindProcess/KindAgent/KindSession → error `codex: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan` (`%s` = kind); SessionPinned → error `codex: cannot start a session with a pre-issued ID`; wrong Options type → `codex: spec.Options is %T, want codex.Options`; Channels/DevChannels non-empty → `codex: channel plugins are not supported; use leo's MCP tools for messaging`.
  - `TestCodexEnv` → `Env(anySpec)` returns `nil, nil`.

  `parse_test.go` loads the Task 0 fixtures:

```go
func TestParseEventsFixtures(t *testing.T) {
	tests := []struct {
		file string
		want harness.Result
	}{
		{"fresh.jsonl", harness.Result{SessionID: "019f4eba-a1a6-77b0-be48-091cd08350e9", Text: "pong"}},
		{"resume.jsonl", harness.Result{SessionID: "019f4eba-a1a6-77b0-be48-091cd08350e9", Text: "pong"}},
		{"badmodel.jsonl", harness.Result{
			SessionID: "019f4ebb-41e4-7b61-a83c-ba50db2be2cd",
			IsError:   true,
			// item error + top-level error + turn.failed, in stream order
			Errors: []string{ /* copy the three message strings verbatim from the fixture */ },
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := Codex{}.ParseEvents(f)
			if err != nil {
				t.Fatalf("ParseEvents: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
```

  Plus: `mcp_tool_call.jsonl` (successful calls) → Text is the final agent_message, no Errors, IsError false; `mcp_cancelled.jsonl` → Text `"0"`, Errors `["user cancelled MCP tool call","user cancelled MCP tool call"]`, IsError false (item-level failures are recorded but never fatal on their own); empty stream → zero Result; garbage lines skipped.

Run: `go test -race ./internal/harness/codex/` — FAIL (package doesn't exist).

- [ ] **Step 2: Implement.** `codex.go`:

```go
// Package codex adapts leo's harness-neutral LaunchSpec to the OpenAI Codex
// CLI. One-shot tasks only (codex exec); session drivers land in a later
// plan. Headless exec always runs with approval policy "never" (upstream
// removed the flag), so the only permission knob is the sandbox.
package codex

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Codex is the OpenAI Codex adapter.
type Codex struct{}

func init() { harness.Register(Codex{}) }

func (Codex) Name() string   { return "codex" }
func (Codex) Binary() string { return "codex" }

// ValidateModel is a format check only: codex model names are validated
// server-side (invalid ones fail the run with a model_not_found error).
func (Codex) ValidateModel(model string) error {
	if model == "" || !strings.ContainsAny(model, " \t") {
		return nil
	}
	return fmt.Errorf("%q is not valid (must not contain whitespace)", model)
}

func (Codex) SupportsChannels() bool { return false }

// SupportsKind: one-shot tasks only until the TurnDriver lands (Plan 4).
func (Codex) SupportsKind(k harness.Kind) bool { return k == harness.KindTask }

// Env: codex needs no adapter-injected env. Auth (CODEX_API_KEY or ambient
// login state) is the caller's/user's concern.
func (Codex) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }

// SessionArgs renders the resume subcommand tokens. Args() places them
// between the flags and the positional prompt; codex has no flag-style
// session selection and cannot pin a fresh session ID.
func (Codex) SessionArgs(s harness.SessionState) []string {
	if s.Mode == harness.SessionResume {
		return []string{"resume", s.ID}
	}
	return nil
}

func (c Codex) Args(spec harness.LaunchSpec) ([]string, error) {
	if spec.Kind != harness.KindTask {
		return nil, fmt.Errorf("codex: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan", spec.Kind)
	}
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("codex: spec.Options is %T, want codex.Options", spec.Options)
	}
	if len(spec.Channels) > 0 || len(spec.DevChannels) > 0 {
		return nil, fmt.Errorf("codex: channel plugins are not supported; use leo's MCP tools for messaging")
	}
	if spec.Session.Mode == harness.SessionPinned {
		return nil, fmt.Errorf("codex: cannot start a session with a pre-issued ID")
	}

	// --skip-git-repo-check always: leo workspaces are leo-managed
	// directories, frequently not git repos, and codex refuses to run in
	// them otherwise.
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if opts.Sandbox != "" {
		args = append(args, "--sandbox", opts.Sandbox)
	}
	args = append(args, opts.LeoMCP.configArgs()...)
	args = append(args, c.SessionArgs(spec.Session)...)
	return append(args, spec.Prompt), nil
}
```

`options.go`:

```go
package codex

import (
	"fmt"
	"sort"
	"strings"
)

// harness_options keys accepted by the codex adapter.
var optionKeys = []string{"sandbox"}

var validSandboxes = map[string]bool{
	"read-only": true, "workspace-write": true, "danger-full-access": true,
}

// Options carries the codex-specific knobs. LeoMCP is runtime-only, filled
// by the task runner when leo's MCP server is wired in.
type Options struct {
	Sandbox string // "" = codex default (read-only)
	LeoMCP  *LeoMCPBridge
}

// LeoMCPBridge describes the per-invocation `-c mcp_servers.leo.*` config
// overrides that register leo's MCP server for one codex run. EnvVars is a
// parent-env whitelist (values stay out of ps-visible argv); ApprovalMode
// "approve" is required or headless exec auto-cancels every MCP tool call.
type LeoMCPBridge struct {
	Command      string
	Args         []string
	EnvVars      []string
	ApprovalMode string
}

// configArgs renders the bridge as repeated -c key=value overrides using
// TOML value syntax (strings quoted, arrays bracketed).
func (b *LeoMCPBridge) configArgs() []string {
	if b == nil {
		return nil
	}
	return []string{
		"-c", fmt.Sprintf("mcp_servers.leo.command=%s", tomlString(b.Command)),
		"-c", fmt.Sprintf("mcp_servers.leo.args=%s", tomlStringArray(b.Args)),
		"-c", fmt.Sprintf("mcp_servers.leo.env_vars=%s", tomlStringArray(b.EnvVars)),
		"-c", fmt.Sprintf("mcp_servers.leo.default_tools_approval_mode=%s", tomlString(b.ApprovalMode)),
	}
}

func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func tomlStringArray(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, tomlString(it))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// DecodeOptions strictly decodes a harness_options map into Options.
// Removed/unsupported knobs get pointed rejections rather than the generic
// unknown-key error. Keys are processed in sorted order so multi-error maps
// fail deterministically.
func (Codex) DecodeOptions(raw map[string]any) (any, error) {
	var o Options
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
		var err error
		switch key {
		case "sandbox":
			var s string
			if s, err = stringOption(key, val); err == nil {
				if s != "" && !validSandboxes[s] {
					err = fmt.Errorf("sandbox %q is not valid (use read-only, workspace-write, or danger-full-access)", s)
				} else {
					o.Sandbox = s
				}
			}
		case "approval":
			err = fmt.Errorf("option %q is not supported: codex exec always runs non-interactively (approval policy %q)", key, "never")
		case "append_system_prompt":
			err = fmt.Errorf("option %q is not supported: codex has no append-system-prompt equivalent (use the workspace AGENTS.md)", key)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

func stringOption(key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("option %q must be a string, got %T", key, val)
	}
	return s, nil
}
```

`parse.go`:

```go
package codex

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// event is the subset of `codex exec --json` JSONL events leo consumes.
// Full schema: codex-rs/exec/src/exec_events.rs (thread.started,
// turn.started/completed/failed, item.started/updated/completed, error).
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"` // type=="error"
	Error    struct {
		Message string `json:"message"`
	} `json:"error"` // type=="turn.failed"
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`    // item type "agent_message"
		Message string `json:"message"` // item type "error"
		Error   struct {
			Message string `json:"message"`
		} `json:"error"` // e.g. failed mcp_tool_call
	} `json:"item"`
}

// ParseEvents folds a codex exec --json stream into a Result. The last
// agent_message wins as the result text. Fatal signals are top-level
// "error" events and "turn.failed"; item-level errors (including failed
// tool calls) are recorded but not fatal on their own — codex signals
// fatality via exit code and the top-level events. EOF ends the turn;
// unparseable lines are skipped.
func (Codex) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var res harness.Result
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt event
		if json.Unmarshal(line, &evt) != nil {
			continue
		}
		switch evt.Type {
		case "thread.started":
			res.SessionID = evt.ThreadID
		case "error":
			res.IsError = true
			if evt.Message != "" {
				res.Errors = append(res.Errors, evt.Message)
			}
		case "turn.failed":
			res.IsError = true
			if evt.Error.Message != "" {
				res.Errors = append(res.Errors, evt.Error.Message)
			}
		case "item.completed":
			switch evt.Item.Type {
			case "agent_message":
				res.Text = evt.Item.Text
			case "error":
				if evt.Item.Message != "" {
					res.Errors = append(res.Errors, evt.Item.Message)
				}
			default:
				if evt.Item.Error.Message != "" {
					res.Errors = append(res.Errors, evt.Item.Error.Message)
				}
			}
		}
	}
	return res, nil
}
```

- [ ] **Step 3: Run to verify PASS**: `go test -race -cover ./internal/harness/codex/` — green, ≥80%.

- [ ] **Step 4: Full suite + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/harness/codex/
git commit -m "feat(harness): codex adapter — one-shot exec with MCP bridge via -c overrides"
```

---

### Task 3: Opencode adapter

**Files:**
- Create: `internal/harness/opencode/opencode.go`
- Create: `internal/harness/opencode/options.go`
- Create: `internal/harness/opencode/parse.go`
- Create: `internal/harness/opencode/opencode_test.go`, `options_test.go`, `parse_test.go`
- (testdata pre-seeded by Task 0)

**Interfaces:**
- Consumes: `harness.Register`, `harness.LaunchSpec`, `harness.Result`.
- Produces: `opencode.Opencode{}` registered as `"opencode"`; `opencode.Options{Permission map[string]any; LeoMCP *LeoMCPBridge}`; `opencode.LeoMCPBridge{Command []string; Env map[string]string}`. Task 5 constructs `LeoMCPBridge` (with the LEO_* env values) and type-switches on `opencode.Options`.

- [ ] **Step 1: Write failing tests.** `options_test.go`:
  - `{"permission":{"bash":"deny","edit":"allow","webfetch":"ask"}}` decodes.
  - Pattern-map values decode: `{"permission":{"bash":{"git *":"allow","*":"ask"}}}`.
  - Bad leaf value errors: `permission value %q for "bash" is not valid (use allow, ask, or deny)`; bad nested leaf same message with the pattern key; non-string/non-map value errors `option "permission" values must be "allow"/"ask"/"deny" or a pattern map, got %T for "bash"`; `permission: "x"` errors `option "permission" must be a map, got string`.
  - `{"append_system_prompt":"x"}` errors: `option "append_system_prompt" is not supported: opencode has no append-system-prompt equivalent (use AGENTS.md or the instructions config)`.
  - Unknown key errors `unknown option "foo" (valid: permission)`; nil map → zero Options.

  `opencode_test.go`:
  - `TestValidateModel`: `""` ok; `"anthropic/claude-sonnet-4-5"` ok; `"opus"`, `"a/"`, `"/b"` error `%q is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`.
  - `TestSupportsChannels` false; `TestSupportsKind` task-only; `TestSessionArgs`: resume → `["-s","ses_1"]`, none/pinned → nil.
  - `TestArgs` golden table:

```go
{
	name: "fresh minimal",
	spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "do it", Options: Options{}},
	want: []string{"run", "--format", "json", "do it"},
},
{
	name: "model and resume",
	spec: harness.LaunchSpec{
		Kind: harness.KindTask, Prompt: "again", Model: "anthropic/claude-sonnet-4-5",
		Session: harness.SessionState{Mode: harness.SessionResume, ID: "ses_42"},
		Options: Options{},
	},
	want: []string{"run", "--format", "json",
		"--model", "anthropic/claude-sonnet-4-5", "-s", "ses_42", "again"},
},
```

  Error cases mirror codex (kind/pinned/options-type/channels) with `opencode:` prefixes: `opencode: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan`, `opencode: cannot start a session with a pre-issued ID`, `opencode: spec.Options is %T, want opencode.Options`, `opencode: channel plugins are not supported; use leo's MCP tools for messaging`.
  - `TestEnv` — the load-bearing one:

```go
func TestEnvBuildsConfigContent(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{
		Permission: map[string]any{"bash": "deny"},
		LeoMCP: &LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env:     map[string]string{"LEO_PROCESS_NAME": "task:t", "LEO_WEB_PORT": "8080", "LEO_API_TOKEN": "tok"},
		},
	}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := env["OPENCODE_CONFIG_CONTENT"]
	var cfg struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Enabled     bool              `json:"enabled"`
			Environment map[string]string `json:"environment"`
		} `json:"mcp"`
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config content is not JSON: %v\n%s", err, raw)
	}
	leo := cfg.MCP["leo"]
	if leo.Type != "local" || !leo.Enabled || !reflect.DeepEqual(leo.Command, []string{"leo", "mcp-server"}) {
		t.Errorf("mcp.leo = %+v", leo)
	}
	if leo.Environment["LEO_API_TOKEN"] != "tok" {
		t.Errorf("environment = %+v", leo.Environment)
	}
	if cfg.Permission["bash"] != "deny" {
		t.Errorf("permission = %+v", cfg.Permission)
	}
}
```

  Plus: permission-only (no LeoMCP) → content has `permission` and no `mcp`; LeoMCP-only → `mcp` and no `permission`; neither → `Env` returns nil map.

  `parse_test.go` over the Task 0 fixtures (same shape as codex's): `fresh.jsonl` → `{SessionID:"ses_0b15975f3ffeiBHJ9UNhtqbZzJ", Text:"pong"}`; `resume.jsonl` → same session, `"pong"`; `truncated_no_step_finish.jsonl` → **identical Result to fresh.jsonl** (EOF is end-of-turn; step_finish never required); `multistep_deny.jsonl` → Text `"BLOCKED"` (its only text event); `badmodel.jsonl` → SessionID from the error events (`ses_…` — copy verbatim from the fixture), IsError true, Errors carrying both error `data.message` strings in stream order, non-JSON log blob lines skipped; a synthetic two-text-event stream → texts joined with `"\n"`.

Run: `go test -race ./internal/harness/opencode/` — FAIL.

- [ ] **Step 2: Implement.** `opencode.go`:

```go
// Package opencode adapts leo's harness-neutral LaunchSpec to the opencode
// CLI. One-shot tasks only (opencode run); the server-based session driver
// lands in a later plan. Permissions are config-only upstream, so they ride
// in via the OPENCODE_CONFIG_CONTENT env overlay rather than argv.
package opencode

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Opencode is the opencode adapter.
type Opencode struct{}

func init() { harness.Register(Opencode{}) }

func (Opencode) Name() string   { return "opencode" }
func (Opencode) Binary() string { return "opencode" }

// ValidateModel enforces opencode's required provider/model shape.
func (Opencode) ValidateModel(model string) error {
	if model == "" {
		return nil
	}
	provider, name, ok := strings.Cut(model, "/")
	if ok && provider != "" && name != "" {
		return nil
	}
	return fmt.Errorf("%q is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)", model)
}

func (Opencode) SupportsChannels() bool { return false }

// SupportsKind: one-shot tasks only until the ServerDriver lands (Plan 4).
func (Opencode) SupportsKind(k harness.Kind) bool { return k == harness.KindTask }

func (Opencode) SessionArgs(s harness.SessionState) []string {
	if s.Mode == harness.SessionResume {
		return []string{"-s", s.ID}
	}
	return nil
}

func (o Opencode) Args(spec harness.LaunchSpec) ([]string, error) {
	if spec.Kind != harness.KindTask {
		return nil, fmt.Errorf("opencode: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan", spec.Kind)
	}
	if _, ok := spec.Options.(Options); !ok {
		return nil, fmt.Errorf("opencode: spec.Options is %T, want opencode.Options", spec.Options)
	}
	if len(spec.Channels) > 0 || len(spec.DevChannels) > 0 {
		return nil, fmt.Errorf("opencode: channel plugins are not supported; use leo's MCP tools for messaging")
	}
	if spec.Session.Mode == harness.SessionPinned {
		return nil, fmt.Errorf("opencode: cannot start a session with a pre-issued ID")
	}

	args := []string{"run", "--format", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, o.SessionArgs(spec.Session)...)
	return append(args, spec.Prompt), nil
}
```

`options.go`:

```go
package opencode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// harness_options keys accepted by the opencode adapter.
var optionKeys = []string{"permission"}

var validPermissionValues = map[string]bool{"allow": true, "ask": true, "deny": true}

// Options carries the opencode-specific knobs. LeoMCP is runtime-only,
// filled by the task runner when leo's MCP server is wired in.
type Options struct {
	Permission map[string]any // tool → "allow"|"ask"|"deny", or pattern map of the same
	LeoMCP     *LeoMCPBridge
}

// LeoMCPBridge describes the leo MCP server entry injected into the
// per-spawn OPENCODE_CONFIG_CONTENT overlay (deep-merged over the user's
// own opencode config; no file mutation).
type LeoMCPBridge struct {
	Command []string
	Env     map[string]string
}

// DecodeOptions strictly decodes a harness_options map into Options. Keys
// are processed in sorted order so multi-error maps fail deterministically.
func (Opencode) DecodeOptions(raw map[string]any) (any, error) {
	var o Options
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
		var err error
		switch key {
		case "permission":
			o.Permission, err = permissionOption(val)
		case "append_system_prompt":
			err = fmt.Errorf("option %q is not supported: opencode has no append-system-prompt equivalent (use AGENTS.md or the instructions config)", key)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

func permissionOption(val any) (map[string]any, error) {
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("option %q must be a map, got %T", "permission", val)
	}
	out := make(map[string]any, len(m))
	for tool, v := range m {
		switch tv := v.(type) {
		case string:
			if !validPermissionValues[tv] {
				return nil, fmt.Errorf("permission value %q for %q is not valid (use allow, ask, or deny)", tv, tool)
			}
			out[tool] = tv
		case map[string]any:
			patterns := make(map[string]any, len(tv))
			for pat, pv := range tv {
				s, ok := pv.(string)
				if !ok || !validPermissionValues[s] {
					return nil, fmt.Errorf("permission value %q for %q is not valid (use allow, ask, or deny)", fmt.Sprint(pv), pat)
				}
				patterns[pat] = s
			}
			out[tool] = patterns
		default:
			return nil, fmt.Errorf("option %q values must be %q/%q/%q or a pattern map, got %T for %q", "permission", "allow", "ask", "deny", v, tool)
		}
	}
	return out, nil
}

// Env builds the OPENCODE_CONFIG_CONTENT overlay for a launch: the leo MCP
// server entry (when wired) plus the user's permission map. Returns nil when
// there is nothing to inject.
func (Opencode) Env(spec harness.LaunchSpec) (map[string]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("opencode: spec.Options is %T, want opencode.Options", spec.Options)
	}
	if opts.LeoMCP == nil && len(opts.Permission) == 0 {
		return nil, nil
	}
	cfg := map[string]any{}
	if opts.LeoMCP != nil {
		cfg["mcp"] = map[string]any{
			"leo": map[string]any{
				"type":        "local",
				"command":     opts.LeoMCP.Command,
				"enabled":     true,
				"environment": opts.LeoMCP.Env,
			},
		}
	}
	if len(opts.Permission) > 0 {
		cfg["permission"] = opts.Permission
	}
	content, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("opencode: marshaling config content: %w", err)
	}
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(content)}, nil
}
```

`parse.go`:

```go
package opencode

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// event is the subset of `opencode run --format json` JSONL events leo
// consumes. Every event carries the session ID; text lives in part.text.
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text string `json:"text"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// ParseEvents folds an opencode run --format json stream into a Result.
// Text events accumulate (joined with newlines) — multi-step turns emit
// several. EOF is authoritative end-of-turn: the final step_finish may be
// absent (upstream #26855 on older versions and --attach), so it is never
// required. Non-JSON lines (opencode interleaves log output on errors) are
// skipped.
func (Opencode) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var res harness.Result
	var texts []string
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt event
		if json.Unmarshal(line, &evt) != nil {
			continue
		}
		if res.SessionID == "" && evt.SessionID != "" {
			res.SessionID = evt.SessionID
		}
		switch evt.Type {
		case "text":
			if evt.Part.Text != "" {
				texts = append(texts, evt.Part.Text)
			}
		case "error":
			res.IsError = true
			msg := evt.Error.Data.Message
			if msg == "" {
				msg = evt.Error.Name
			}
			if msg != "" {
				res.Errors = append(res.Errors, msg)
			}
		}
	}
	res.Text = strings.Join(texts, "\n")
	return res, nil
}
```

- [ ] **Step 3: Run to verify PASS**: `go test -race -cover ./internal/harness/opencode/` — green, ≥80%.

- [ ] **Step 4: Full suite + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/harness/opencode/
git commit -m "feat(harness): opencode adapter — one-shot run with OPENCODE_CONFIG_CONTENT overlay"
```

---

### Task 4: Config — register adapters, kind-support validation, cross-harness model cascade

**Files:**
- Modify: `internal/config/harness.go` (blank imports; `TaskModel` harness-awareness)
- Modify: `internal/config/config.go` (Validate: SupportsKind checks; move `TaskModel` if it lives there)
- Modify: `internal/config/harness_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: registered `codex`/`opencode` adapters; `h.SupportsKind`.
- Produces: `harness.Names()` now returns `[claude codex opencode]` — **grep for tests asserting the registered-names list or the `(available: claude)` error text and update them**. `TaskModel` semantics change (below); Task 5 depends on it.

- [ ] **Step 1: Blank imports.** In `internal/config/harness.go`:

```go
import (
	// Adapters self-register in init; config validation must be able to
	// resolve them.
	_ "github.com/blackpaw-studio/leo/internal/harness/claude"
	_ "github.com/blackpaw-studio/leo/internal/harness/codex"
	_ "github.com/blackpaw-studio/leo/internal/harness/opencode"
)
```

- [ ] **Step 2: Write failing validation tests** (extend `config_test.go`/`harness_test.go`, table-driven). A minimal valid config + one mutation per case:
  - `processes.builder.harness: codex` → error `processes.builder.harness: the codex harness cannot run supervised processes yet (only scheduled tasks) — see docs/configuration/harnesses.md`
  - `templates.helper.harness: opencode` → `templates.helper.harness: the opencode harness cannot run ephemeral agents yet (only scheduled tasks) — see docs/configuration/harnesses.md`
  - `sessions.chat.harness: codex` → `sessions.chat.harness: the codex harness cannot run persistent sessions yet (only scheduled tasks) — see docs/configuration/harnesses.md`
  - task with `runtime: persistent` + `harness: opencode` → `tasks.nightly.harness: the opencode harness cannot run persistent tasks yet (persistent tasks run through sessions) — see docs/configuration/harnesses.md`
  - `defaults.harness: codex` with a plain process defined → the process errors (inherited harness lacks process support).
  - Happy path: `tasks.nightly.harness: codex` + `harness_options: {sandbox: workspace-write}` + `model: gpt-5.3-codex` validates clean; same for opencode with `model: anthropic/claude-sonnet-4-5` + `permission: {bash: allow}`.
  - Model delegation: `tasks.t.harness: opencode` + `model: opus` → `tasks.t.model "opus" is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`; `tasks.t.harness: codex` + `model: "gpt 5"` → whitespace error.
  - Channels: `tasks.t.harness: codex` + `channels: [...]` already errors via the existing SupportsChannels check — add one regression case asserting the exact existing message with `codex` in it.

- [ ] **Step 3: Implement the Validate() additions.** In each scope loop, inside the existing `if h, ok := resolveHarness(...); ok {` block, add (adjusting scope word and kind):

```go
		if !h.SupportsKind(harness.KindProcess) {
			errs = append(errs, fmt.Sprintf("processes.%s.harness: the %s harness cannot run supervised processes yet (only scheduled tasks) — see docs/configuration/harnesses.md", name, h.Name()))
		}
```

templates → `harness.KindAgent` / "cannot run ephemeral agents yet"; sessions → `harness.KindSession` / "cannot run persistent sessions yet"; tasks → only when `task.Runtime == "persistent"`, check `harness.KindSession` / "cannot run persistent tasks yet (persistent tasks run through sessions)".

- [ ] **Step 4: Cross-harness model cascade.** Write failing tests first: with `defaults: {model: opus}` (claude defaults) and `tasks.t.harness: codex` with no model → `cfg.TaskModel(t)` returns `""` (not "opus", not the built-in default); with task harness == defaults harness, behavior is unchanged (task → defaults → `DefaultModel`). Then change `TaskModel`:

```go
// TaskModel resolves the model for a task: task → defaults → built-in.
// The defaults/built-in fall-through only applies when the task runs the
// same harness as defaults — model names are harness-specific, so a claude
// default like "opus" must not leak into a codex task. Empty means the
// harness picks its own default model.
func (c *Config) TaskModel(t TaskConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if c.TaskHarness(t) != c.DefaultsHarness() {
		return ""
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}
```

(Location: wherever `TaskModel` currently lives — keep it there. `ProcessModel`/`TemplateModel`/`SessionModel` are untouched: non-claude is unreachable for those scopes until Plan 4, enforced by Step 3's validation.)

- [ ] **Step 5: Verify + commit**

```bash
go test -race -cover ./internal/config/ && go test -race ./... && make lint && make e2e
git add internal/config/
git commit -m "feat(config): register codex/opencode; kind-support validation; harness-scoped model cascade"
```

---

### Task 5: Runner — per-harness binary, env, parsing, prereq

**Files:**
- Modify: `internal/run/runner.go`
- Modify: `internal/run/runner_test.go`, `internal/run/args_test.go`

**Interfaces:**
- Consumes: everything above. `leomcp.AppendArg`, `leomcp.MergeSystemPrompt` (claude branch only).
- Produces: `buildArgs(cfg, task, taskName, prompt, sessionID string, leoEnv map[string]string) (args []string, extraEnv map[string]string)` — the `leoMCPOK bool` param becomes `leoEnv` (nil = gated off; non-nil carries the three `LEO_*` vars from `leoMCPEnv`). `executeCommand` gains a leading `binary string` param. Package var `claudeBinary` is deleted; `lookPathFn = exec.LookPath` seam added.

**Behavior notes (all tested below):**
1. `notifyFailure` stays claude-only by construction (it fires only for channel tasks, and channels validate claude-only). It passes `claudeharness.Claude{}.Binary()` and explicitly adds `CLAUDE_CODE_ENTRYPOINT=cli` to its env since `executeCommand` no longer injects it.
2. The channel-init monitor is claude stream-json-specific but only arms when `channelPrefixes` is non-empty — impossible for non-claude tasks (validation). No change needed.
3. `isSessionError` gains one pattern: `"no rollout found"` (codex stale thread; stderr `thread/resume failed: no rollout found for thread id …` — note codex says "thread", never "session", so the existing patterns miss it). Opencode's `Error: Session not found` already matches the existing `session`+`not found` pattern via the raw-output fallback (its stdout is empty on stale sessions, so the fallback fires).
4. New failure mode: `execErr == nil && result.IsError` → the attempt fails with error `fmt.Errorf("harness reported error: %s", strings.Join(result.Errors, "; "))` (opencode has known exit-0-on-error bugs). This is a deliberate cross-harness behavior change; claude exits nonzero on real errors so it is unreachable there in practice.
5. Per-harness prereq at task start: after resolving the harness in `Run()` (before the lock), `if _, err := lookPathFn(h.Binary()); err != nil { return fmt.Errorf("task %q uses the %s harness, but %q was not found in PATH — install it or change the task's harness", taskName, h.Name(), h.Binary()) }`. Same check in `Preview` is skipped (previews must work without the binary).

- [ ] **Step 1: Characterization first.** `TestBuildArgsCharacterization` in `args_test.go`: change call sites to the new signature (`leoEnv` nil / non-nil replacing `leoMCPOK` false/true) and ignore the second return; **argv expectations for all existing claude cases unchanged**. Run: FAIL (signature).

- [ ] **Step 2: Rewire `buildArgs`.** Resolve harness → decode options → type-switch runtime fill → `h.Args(spec)` + `h.Env(spec)`:

```go
func buildArgs(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID string, leoEnv map[string]string) ([]string, map[string]string) {
	h, err := harness.Get(cfg.TaskHarness(task))
	if err != nil {
		log.Printf("[task:%s] resolving harness: %v", taskName, err)
		return nil, nil
	}
	decoded, err := h.DecodeOptions(cfg.TaskHarnessOptions(task))
	if err != nil {
		log.Printf("[task:%s] decoding harness options: %v", taskName, err)
		return nil, nil
	}
	leoMCPOK := leoEnv != nil

	sess := harness.SessionState{}
	if sessionID != "" {
		sess = harness.SessionState{Mode: harness.SessionResume, ID: sessionID}
	}
	spec := harness.LaunchSpec{
		Kind:        harness.KindTask,
		Name:        taskName,
		Model:       cfg.TaskModel(task),
		MaxTurns:    cfg.TaskMaxTurns(task),
		Workspace:   cfg.TaskWorkspace(task),
		DevChannels: task.DevChannels,
		Prompt:      prompt,
		Session:     sess,
	}

	switch opts := decoded.(type) {
	case claudeharness.Options:
		mcpConfig := ""
		if p := cfg.TaskMCPConfigPath(task); config.HasMCPServers(p) {
			mcpConfig = p
		}
		var leoMCPArgs []string
		if leoMCPOK {
			leoMCPArgs = leomcp.AppendArg(nil, cfg)
		}
		opts.AppendSystemPrompt = leomcp.MergeSystemPrompt(cfg, opts.AppendSystemPrompt)
		opts.MCPConfigPath = mcpConfig
		opts.LeoMCPArgs = leoMCPArgs
		spec.Options = opts
	case codexharness.Options:
		if leoMCPOK {
			opts.LeoMCP = &codexharness.LeoMCPBridge{
				Command:      "leo",
				Args:         []string{"mcp-server"},
				EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
				ApprovalMode: "approve",
			}
		}
		spec.Options = opts
	case opencodeharness.Options:
		if leoMCPOK {
			opts.LeoMCP = &opencodeharness.LeoMCPBridge{
				Command: []string{"leo", "mcp-server"},
				Env:     leoEnv,
			}
		}
		spec.Options = opts
	default:
		log.Printf("[task:%s] harness %q returned unsupported options type %T", taskName, h.Name(), decoded)
		return nil, nil
	}

	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[task:%s] building %s args: %v", taskName, h.Name(), err)
		return nil, nil
	}
	env, err := h.Env(spec)
	if err != nil {
		log.Printf("[task:%s] building %s env: %v", taskName, h.Name(), err)
		return nil, nil
	}
	return args, env
}
```

(`MaxTurns` reaches the spec for every harness; codex/opencode ignore it by design — documented in Task 7.)

- [ ] **Step 3: Thread through Run/Preview/runTaskAttempt/executeCommand.**
  - `Run()`: resolve `h, err := harness.Get(cfg.TaskHarness(task))` right after `resolveTask` (return the error); add the `lookPathFn` prereq check (note 5). Replace `parseClaudeOutput` shim usage with `h.ParseEvents(bytes.NewReader(output))` inside `runTaskAttempt` (pass `h` in). Merge order for spawn env: `mergeEnvMaps(task.Env, harnessEnv, leoEnv)` — leo wiring still wins over task env; harness env sits between (a task can deliberately override `CLAUDE_CODE_ENTRYPOINT`; it must never shadow the leo MCP vars).
  - `runTaskAttempt(...)` gains `h harness.Harness` and the extraEnv from buildArgs; calls `executeCommand(ctx, h.Binary(), ...)`.
  - `executeCommand(ctx context.Context, binary, workDir string, args []string, ...)`: `cmd := execCommand(binary, args...)`; delete the unconditional `CLAUDE_CODE_ENTRYPOINT` append (claude's Env carries it now).
  - `notifyFailure`: `executeCommand(ctx, claudeharness.Claude{}.Binary(), ...)` with `CLAUDE_CODE_ENTRYPOINT=cli` merged into its `extraEnv`.
  - Delete the `claudeBinary` package var. Add `var lookPathFn = exec.LookPath`.
  - Failure semantics (note 4): in `Run()`'s attempt loop, after `ar := runTaskAttempt(...)`, insert:

```go
		if ar.execErr == nil && ar.result.IsError {
			ar.execErr = fmt.Errorf("harness reported error: %s", strings.Join(ar.result.Errors, "; "))
		}
```

  - `isSessionError` (note 3): add to `sessionErrorText`:

```go
	if strings.Contains(text, "no rollout found") {
		return true
	}
```

- [ ] **Step 4: Tests.** In `runner_test.go` / `args_test.go` (table-driven; use the existing `execCommand` swap seam and `t.Setenv`-style env capture via `exec.Command("env")` where the existing tests do):
  - New buildArgs cases: codex task (harness: codex, sandbox option, no session) → exact argv from Task 2's golden order and nil env; codex with `leoEnv` non-nil → argv includes the four `-c` pairs; opencode task with permission + leoEnv → argv `run --format json … prompt` and env containing `OPENCODE_CONFIG_CONTENT` (unmarshal and spot-check `mcp.leo.environment.LEO_API_TOKEN`); opencode/codex with `defaults.model: opus` and no task model → argv contains **no** `--model` (Task 4's cascade).
  - Claude env characterization: a claude task run's child env still contains `CLAUDE_CODE_ENTRYPOINT=cli` (via the merged harness env — extend the existing env-passthrough test at ~runner_test.go:1043).
  - Prereq: stub `lookPathFn` to fail → `Run` returns the exact message from note 5 and `execCommand` is never invoked (guard with a boolean in the stub).
  - IsError failure: stub execCommand to emit an opencode `error`-event stream with exit 0 → `Run` returns error containing `harness reported error:`; history records a failure.
  - Codex stale-thread retry: stub execCommand to fail (exit 1) printing `Error: thread/resume: thread/resume failed: no rollout found for thread id x (code -32600)` on first call, succeed on second; assert the in-place retry ran without `resume` tokens and the stale session was cleared from the store.
  - Session persistence: codex stream fixture → `sessions.Get("task:t")` returns the thread_id after Run.

- [ ] **Step 5: Full suite + commit**

```bash
go test -race -cover ./internal/run/ && go test -race ./... && make lint && make e2e
git add internal/run/
git commit -m "feat(run): per-harness binary, env, parsing, and prereq in the task runner"
```

---

### Task 6: E2E — fake binaries + gated real smoke

**Files:**
- Create: `e2e/fakecodex/main.go`
- Create: `e2e/fakeopencode/main.go`
- Create: `e2e/harness_task_test.go` (build tag `e2e`)
- Modify: `e2e/e2e_test.go` (TestMain builds the two new fakes into the same PATH dir as fakeclaude)

**Interfaces:**
- Consumes: the runner's final argv/env contracts from Task 5.
- Produces: deterministic CI coverage of `leo run` on all three harnesses.

- [ ] **Step 1: fakecodex.** Mirrors `e2e/fakeclaude/main.go`'s conventions (arg/env logging via `FAKECODEX_ARGLOG`/`FAKECODEX_ENVLOG`, scenario via `FAKECODEX_SCENARIO`: `success` (default) | `error`):
  - `--version` → print `codex-cli 0.144.1-fake`, exit 0.
  - Otherwise expect argv starting `exec --json --skip-git-repo-check`; if `resume <id>` tokens present, echo that id as the thread_id, else emit `thread_fake_1`.
  - success: print the four-line fresh-stream shape (thread.started / turn.started / item.completed agent_message `"fake codex done"` / turn.completed), exit 0.
  - error: print thread.started + top-level error + turn.failed (badmodel shape), exit 1.

- [ ] **Step 2: fakeopencode.** Same conventions (`FAKEOPENCODE_*`):
  - `--version` → `1.17.7-fake`.
  - Expect argv starting `run --format json`; session id from `-s <id>` if present else `ses_fake000000000000000000001`.
  - success: step_start / text (`"fake opencode done"`) / step_finish lines, exit 0. Scenario `truncated`: omit step_finish (still exit 0). Scenario `error`: one non-JSON log line + an `error` event, exit 1.

- [ ] **Step 3: e2e tests** in `harness_task_test.go` (follow the existing `e2e_test.go` task-run test patterns for daemonless `leo run` invocation, temp leo home, task config fixture):
  - `TestCodexTaskRun`: config with `tasks.cx: {schedule, prompt_file, harness: codex, model: gpt-5.3-codex, harness_options: {sandbox: workspace-write}}` → run `leo run cx` with `FAKECODEX_ARGLOG` set → assert exit 0; arg log equals exactly `["exec","--json","--skip-git-repo-check","--model","gpt-5.3-codex","--sandbox","workspace-write","<prompt>"]`; session store contains `thread_fake_1`; **second run's** arg log contains `resume thread_fake_1`.
  - `TestOpencodeTaskRun`: same shape for opencode (`model: anthropic/claude-sonnet-4-5`, `harness_options: {permission: {bash: allow}}`) → argv `["run","--format","json","--model","anthropic/claude-sonnet-4-5","<prompt>"]`; env log contains `OPENCODE_CONFIG_CONTENT` with the permission block; session persisted + resumed with `-s`.
  - `TestOpencodeTruncatedStreamStillSucceeds`: scenario `truncated` → run succeeds, session stored (EOF-as-turn-end e2e proof).
  - `TestCodexTaskErrorRecorded`: scenario `error` → `leo run` exits nonzero, history reason failure.
  - `TestNonClaudeValidationErrors`: config putting `harness: codex` on a process → `leo validate` (or config load) reports the Task 4 message. 
  - `TestRealHarnessSmoke` (one per harness, in the same file): `t.Skip` unless (a) the real binary is on `exec.LookPath` **and** (b) `LEO_E2E_REAL_HARNESSES=1` is set (real runs cost API money — never in CI). Body: trivial one-shot task (`Reply with exactly: pong`), assert exit 0 and a non-empty stored session ID.

- [ ] **Step 4: Verify + commit**

```bash
make e2e && go test -race ./... && make lint
git add e2e/
git commit -m "test(e2e): codex/opencode task runs via fake binaries; gated real smoke"
```

---

### Task 7: Docs

**Files:**
- Modify: `docs/configuration/harnesses.md` (codex + opencode sections)
- Modify: `docs/configuration/config-reference.md` (harness enum, per-harness options)
- Modify: `CLAUDE.md` (repo root — harness list, model validation note)
- Check: `mkdocs.yml` nav (no new files, so likely untouched)

- [ ] **Step 1: harnesses.md.** After the claude option reference, add a section per new harness:
  - **codex**: status (scheduled tasks only — processes/agents/sessions rejected at validation until session drivers land); `harness_options`: `sandbox` (`read-only` default per codex, `workspace-write`, `danger-full-access`); approval is always `never` in headless exec (upstream removed the flag — an `approval:` key is rejected with a pointed error); no append-system-prompt (AGENTS.md in the workspace is codex's mechanism); model is free-form, validated server-side, and **does not inherit `defaults.model` across harnesses** (unset = codex's default model); `max_turns` is ignored; auth via `CODEX_API_KEY` in the task's `env:` or ambient `codex login` state; leo always passes `--skip-git-repo-check` (leo workspaces are often not git repos); resume via codex threads (`codex exec resume <thread-id>` under the hood); MCP bridge: when the web UI is enabled, leo injects its MCP server per-invocation via `-c mcp_servers.leo.*` overrides — including `default_tools_approval_mode="approve"` scoped to the leo server, because headless codex otherwise auto-cancels MCP tool calls; messaging goes through `leo_send_message` (no channel plugins).
  - **opencode**: status line (same); `harness_options`: `permission` map (`allow|ask|deny` per tool, pattern maps supported), enforced via a per-spawn `OPENCODE_CONFIG_CONTENT` overlay (deep-merged; the user's own opencode config is untouched); model **must** be `provider/model`; same cross-harness model-cascade rule; `max_turns` ignored; no append-system-prompt; sessions resume via `-s ses_…`; MCP bridge via the same overlay's `mcp.leo` entry; note the EOF-as-turn-end parser stance (upstream #26855) and that leo treats in-stream `error` events as failures even on exit 0.
  - Add one full YAML example showing a codex task and an opencode task (copy the spec's config sketch, updated: no `approval:` key).
- [ ] **Step 2: config-reference.md** — `harness:` accepted values now `claude | codex | opencode`; per-harness `harness_options` tables (claude's seven keys; codex `sandbox`; opencode `permission`); model column notes per-harness validation.
- [ ] **Step 3: root CLAUDE.md** — update the two spots that say codex/opencode land in later plans: `internal/harness` description ("claude, codex, opencode adapters; codex/opencode are task-only until session drivers land") and the `Config.Validate()` model-names sentence (claude list; codex format-check; opencode provider/model).
- [ ] **Step 4: Verify + commit**

```bash
go test -race ./... && make lint && make e2e
git add docs/ CLAUDE.md
git commit -m "docs: codex and opencode task harnesses"
```

---

## Rollout Notes (not tasks)

- **Flag to Evan in the PR:** the brief's `approval (untrusted|on-request|never)` codex option was dropped — re-verification showed `codex exec` has no approval flag at all (hardcoded `never`; `on-failure` deprecated upstream). The adapter rejects the key with a pointed error.
- **Flag to Evan in the PR:** two deliberate cross-harness behavior changes in the runner (Global Constraints bullet 1): `CLAUDE_CODE_ENTRYPOINT` now injected via the claude adapter's `Env()`, and exit-0-with-error-events now fails the attempt.
- The local codex binary at `/opt/homebrew/bin/codex` may not be linked (the brew cask install wedged on a Gatekeeper scan mid-session; a working copy is at the scratchpad). Real-smoke e2e locally may need `brew install --cask codex` re-run. Do NOT restart any leo services for any of this.
- Evan's live config migration (~21 flat keys) remains a separate, explicitly-gated follow-up — not this branch.
