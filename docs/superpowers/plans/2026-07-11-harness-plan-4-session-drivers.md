# Harness Plan 4: Session Drivers — codex + opencode for Processes, Agents, Persistent Sessions

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-harness `SessionDriver` (Start/Inject/Attach) so `codex` and `opencode` work for supervised processes, ephemeral agents, and persistent sessions — with claude's existing tmux machinery refactored behind the same interface byte-identically.

**Architecture:** The supervisor keeps its restart/backoff loop; drivers tell it *how* a session lives. Three drive styles: claude = **TmuxTUIDriver** (resident TUI in tmux; readiness glyph, dialog dismissal, `--session-id → --resume → fresh` ladder — all moved behind driver hooks, behavior unchanged); codex = **TurnDriver** (`DriveTurns`: no resident process; every injected message spawns `codex exec --json … resume <thread-id> <msg>`); opencode = **ServerDriver** (`DriveTmux`: resident `opencode serve --port <p>` in tmux; injection via `opencode run --attach`; attach via `opencode attach <url>`). Both new drivers' `Inject` blocks until the turn completes and returns a `harness.Result`; the daemon session router uses that for synchronous completion (claude keeps its async Stop-hook path).

**Tech Stack:** Go, stdlib only. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-10-harness-abstraction-design.md` (Plan 4 implements its "Session drivers" + "Session stores" scope)

## Verified CLI facts (2026-07-11, live binaries on this machine)

codex 0.144.1 (`/opt/homebrew/bin/codex`), opencode 1.17.7. Plan-3 facts (docs/superpowers/plans/2026-07-10-harness-plan-3-codex-opencode.md §Verified CLI facts) still hold; new facts verified 2026-07-11. Captured streams live in `~/leo-plan4-fixtures/`. **Do not re-derive these.**

**codex:**
- `codex exec resume [SESSION_ID] [PROMPT]` — session id is a UUID positional; parent flags (`--json`, `--skip-git-repo-check`, `-m`, `-c`, `--sandbox`) come **before** the `resume` subcommand, prompt positional last. Full turn argv verified live 2026-07-10: `exec --json --skip-git-repo-check resume <thread-id> <prompt>`.
- Session lookup filters by **cwd by default** (`--all` disables); an explicit UUID takes precedence, and we always pass one. Turn processes must still run with `Dir = workspace`.
- Sessions on disk: `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO-ts>-<uuid>.jsonl` (verified by listing).
- Stale/unknown thread id: exit 1, empty stdout, stderr `Error: thread/resume: thread/resume failed: no rollout found for thread id <id> (code -32600)` (Plan-3 capture `internal/harness/codex/testdata/` README).
- There is **no way to pin a fresh session ID** — the first turn's `thread.started` event supplies `thread_id`.
- MCP bridge per-turn: the same four `-c mcp_servers.leo.*` overrides as Plan 3, **including `default_tools_approval_mode="approve"`** — without it headless codex auto-cancels MCP calls (verified live 2026-07-10).

**opencode:**
- `opencode serve --port <p> --hostname 127.0.0.1` — headless server. `--port` default 0 = random (useless to us: the only way to learn it is log-scraping, so **leo allocates the port**). Boot log: `opencode server listening on http://127.0.0.1:<p>`; warns `OPENCODE_SERVER_PASSWORD is not set; server is unsecured.` when no password env.
- Health endpoint verified: `GET /global/health` → `{"healthy":true,"version":"1.17.7"}`.
- `opencode run --attach http://127.0.0.1:<p> --format json --dir <workspace> [-s <session-id>] <msg>` — verified live: **blocks until the turn completes server-side, exits 0** … but the attach-mode event forwarding is **lossy**: a completed turn produced ONLY a single `step_start` event (no `text`, no `step_finish`) on stdout (capture `~/leo-plan4-fixtures/oc-attach-fresh.jsonl`). The one event that did arrive carried top-level `sessionID`. Consequences baked into the design: (a) **process exit = turn end** — never wait for `step_finish` (this is the #26855 exclusion); (b) result text from an attached run may legitimately be empty; (c) session-id capture must fall back to `opencode session list` when zero events arrive.
- `opencode session list --format json [-n N]` — verified; returns a JSON array across ALL projects, each entry `{id, title, updated, created, projectId, directory}` — filter by `directory == workspace` and take newest `created` (capture `~/leo-plan4-fixtures/oc-session-list.json`).
- `opencode attach <url> [-s <session-id>] [--dir <dir>]` — interactive TUI against a running server. Auth: `-p/--password` flag or `OPENCODE_SERVER_PASSWORD` env (same for `run --attach`).
- Server API (used as fallback only, not primary): `GET /session?directory=<dir>` lists sessions; `GET /session/<id>/message?directory=<dir>` returns messages. Primary integration stays CLI-first per the spec.
- Stale session id (`-s` unknown): exit 1, empty stdout, stderr `Error: Session not found` (Plan-3 capture).

## Global Constraints

- **Claude behavior stays byte-identical.** Every existing claude flow — process supervision, dialog dismissal, quick-exit ladder, both injection paths (web fast path AND `tmux.InjectPrompt`), attach, idle-suspend, persistent-task routing — must behave exactly as today. Characterization tests must PASS against pre-task code wherever a task says "characterization".
- **The two claude injection paths stay distinct.** The web handler's fast path (`send-keys -l` + `InputHasContent` + Enter) and `tmux.InjectPrompt` (probe + paste) are separate mechanisms; do not merge them while refactoring (this bit us before — see memory `project_two_message_inject_paths`).
- **Channels stay claude-only.** `channels`/`dev_channels` on a non-claude harness remains a validation error. Untouched by this plan.
- Every commit: `go test -race ./...` green, `make lint` clean, **`make e2e` green** (e2e suite is build-tagged; `go test ./...` skips it — bit PR #97 and #98). Changed packages hold ≥80% coverage.
- Run()-level and driver unit tests must stub `lookPathFn`/exec seams — CI runners have no harness binaries on PATH (bit PR #98).
- No mutation of shared harness config files: codex config via `-c` argv, opencode via `OPENCODE_CONFIG_CONTENT`/env. Never write `~/.codex/config.toml` or `opencode.json`.
- Error text conventions unchanged: validation errors keep their exact current shapes except where a task explicitly replaces a "not supported yet" message.
- Commit format `<type>: <description>`, no attribution lines.
- **Never restart the production leo service.** Live testing (if any) uses the isolated test daemon (separate `LEO_HOME`); orchestrator-only, gated tasks.
- The field names `ClaudeArgs` (ProcessSpec/SpawnRequest/agentstore JSON `claude_args`) are **kept** in this plan to avoid a rename cascade; their meaning generalizes to "the harness argv rendered at spawn" (codex: per-turn prefix; opencode: serve command). Renaming is Plan-5-or-later cleanup.

## Design decisions vs the spec (deliberate, reviewed)

1. **The readiness glyph + echo probe stay in `internal/tmux`.** The spec sketches moving them into the claude adapter; this plan wraps `tmux.InjectPrompt` behind `TmuxTUIDriver.Inject` instead and moves only the *decision* logic (dialog dismissal, quick-exit ladder) into the adapter. Rationale: the two live claude injection call sites (web fast path, daemon injector) stay byte-identical, and `internal/tmux` is already claude-only in content. Moving the probe wholesale buys nothing and risks the exact regressions this plan must avoid.
2. **`Inject` returns `*Result`** (nil = async) rather than the spec's bare `error` — the daemon session router needs synchronous completion for turn-based harnesses (no Stop hook exists outside claude).
3. **Server state (`port`, `password`, `model`) for opencode is leo-allocated and persisted** under `state/opencode/` because `opencode serve --port 0` only reveals its random port in a log line (verified) — log-scraping is not an interface.
4. **`opencode serve` runs inside tmux** (DriveTmux) so the existing supervise loop (restart, backoff, post-mortems) is reused unchanged; codex is the only DriveTurns harness.

## Task Ordering

Strictly sequential: 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9. (Contract before claude refactor; claude refactor before supervisor generalization; dispatch before the new drivers so they have call sites; drivers before persistent-session parity; e2e after all behavior; docs last.)

## Not In This Plan (later plans / deferred)

- Web UI: harness dropdown, `OptionsSchema()` forms, driver-aware session pages (Plan 5).
- Idle-suspend for codex/opencode agents — sweep explicitly skips non-claude agents (documented in Task 4); codex has no resident process to suspend and opencode serve panes show no meaningful tmux activity signal.
- Remote-host (`client.hosts`) attach for opencode beyond plain ssh-exec of the attach argv; `-CC` control mode stays claude/tmux-only.
- Migrating Evan's live `~/.leo/leo.yaml` (parked until all plans land + install update — his explicit call 2026-07-11).
- Renaming `ClaudeArgs` fields/JSON keys.
- codex `developer_instructions` config key (still deliberately unused).

---

### Task 0: Pre-seed captured opencode driver fixtures (orchestrator, no subagent)

**Files:**
- Create: `internal/harness/opencode/testdata/attach_fresh.jsonl` (from `~/leo-plan4-fixtures/oc-attach-fresh.jsonl` — the lossy attach-mode stream: a single `step_start` event carrying `sessionID`, from a turn that completed server-side)
- Create: `internal/harness/opencode/testdata/session_list.json` (from `~/leo-plan4-fixtures/oc-session-list.json` — one-entry `session list --format json` output; sanitize the `directory` value to `/tmp/leo-e2e-ws` and keep the real key shape)
- Modify: `internal/harness/opencode/testdata/README.md` (add a paragraph: captured 2026-07-11 from opencode 1.17.7 in `--attach` mode; attach-mode forwarding is lossy — only `step_start` arrived for a completed turn; session_list.json sanitized directory)

- [ ] **Step 1:** Copy + sanitize as above; commit directly on the feature branch:

```bash
git add internal/harness/opencode/testdata
git commit -m "test(harness): captured opencode attach-mode driver fixtures"
```

---

### Task 1: Driver contract + claude TmuxTUIDriver core

**Files:**
- Create: `internal/harness/driver.go`
- Create: `internal/harness/driver_test.go`
- Modify: `internal/harness/harness.go` (add `Driver()` to the interface)
- Modify: `internal/harness/registry_test.go` (fake harness gains `Driver()`)
- Create: `internal/harness/claude/driver.go`
- Create: `internal/harness/claude/driver_test.go`
- Modify: `internal/harness/claude/claude.go` (`Driver()` returns the singleton)
- Modify: `internal/harness/codex/codex.go` + `internal/harness/opencode/opencode.go` (`Driver()` returns nil for now, with a doc comment saying the driver lands in Tasks 5/6)

**Interfaces:**
- Produces (consumed by every later task):

```go
type DriveStyle string
const (
    DriveTmux  DriveStyle = "tmux"  // resident process supervised in a leo tmux session
    DriveTurns DriveStyle = "turns" // no resident process; each Inject spawns a one-shot turn
)

type SessionIDStore interface {
    Get() string
    Set(id string)
    Clear()
}

type SessionHandle struct {
    Kind          Kind
    Name          string            // logical process/agent/session name
    TmuxSession   string            // tmux session name; routing key for driver state files
    Workspace     string
    HomePath      string
    Env           map[string]string // resolved spawn env for driver-spawned helper processes
    TurnArgs      []string          // DriveTurns: rendered per-turn argv prefix (from Args())
    OpeningPrompt string            // delivered by Start for drivers that can't put it in argv
    IDs           SessionIDStore
}

type AttachSpec struct {
    Argv        []string // exec locally, or run verbatim on the remote host over ssh; nil = no live attach
    HistoryPath string   // when Argv is nil: file whose tail is the recent turn history
}

type SessionDriver interface {
    Style() DriveStyle
    Start(ctx context.Context, h SessionHandle) error
    // Inject delivers one message. nil *Result = delivery is asynchronous
    // (claude: completion arrives via the Stop hook / conversation lives in
    // the pane). Non-nil = the turn ran to completion synchronously.
    Inject(ctx context.Context, h SessionHandle, msg string) (*Result, error)
    Attach(h SessionHandle) (AttachSpec, error)
}

// Optional driver capabilities, asserted by the supervisor:
type PaneCare interface {
    PaneKey(pane string) string // tmux key to send for a captured pane, or ""
}

type QuickExitAction int
const (
    QuickExitClearSession     QuickExitAction = iota // clear stored session id (today's default branch)
    QuickExitRetryArgs                               // relaunch with returned args; keep stored id
    QuickExitClearAndNoResume                        // clear stored id AND mark agent no-resume
    QuickExitNone                                    // keep args and stored id (opencode serve crash ≠ poisoned conversation)
)
type QuickExitRecovery interface {
    RecoverQuickExit(args []string) ([]string, QuickExitAction)
}

type TurnAborter interface {
    AbortTurn(h SessionHandle) error // cancel the in-flight injected turn, if any
}
```

- `Harness` interface gains: `Driver() SessionDriver`.
- Claude driver core (this task): `Style() = DriveTmux`; `Start` = no-op returning nil; `Inject` = `tmux.InjectPrompt(ctx, tmuxPath, h.TmuxSession, msg)` then `return nil, nil` (async — nil Result); `Attach` = `AttachSpec{Argv: [tmuxPath, "attach", "-t", target]}` (informational; the CLI keeps its own richer claude attach path — Task 4).

- [ ] **Step 1: Write failing tests** — `internal/harness/driver_test.go`: `QuickExitAction` zero value is `QuickExitClearSession` (locks the iota order — the supervisor's default when a driver lacks the interface):

```go
func TestQuickExitActionZeroValueIsClearSession(t *testing.T) {
    var a QuickExitAction
    if a != QuickExitClearSession {
        t.Fatalf("zero value must be QuickExitClearSession, got %d", a)
    }
}
```

`internal/harness/claude/driver_test.go` (use the `tmux` package's exported test seam? No — the claude driver shells out through its own seam):

```go
func TestTmuxTUIDriverStyle(t *testing.T) {
    if got := (Claude{}).Driver().Style(); got != harness.DriveTmux {
        t.Fatalf("Style() = %q, want %q", got, harness.DriveTmux)
    }
}

func TestTmuxTUIDriverStartIsNoOp(t *testing.T) {
    if err := (Claude{}).Driver().Start(context.Background(), harness.SessionHandle{}); err != nil {
        t.Fatalf("Start: %v", err)
    }
}

func TestTmuxTUIDriverInjectDelegatesToInjectPrompt(t *testing.T) {
    var gotSession, gotBody string
    restore := SetInjectPromptForTest(func(ctx context.Context, tmuxPath, session, body string) error {
        gotSession, gotBody = session, body
        return nil
    })
    defer restore()
    res, err := (Claude{}).Driver().Inject(context.Background(), harness.SessionHandle{TmuxSession: "leo-x"}, "hello")
    if err != nil || res != nil {
        t.Fatalf("Inject = (%v, %v), want (nil, nil)", res, err)
    }
    if gotSession != "leo-x" || gotBody != "hello" {
        t.Fatalf("delegated (%q, %q)", gotSession, gotBody)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/harness/... 2>&1 | head -30`
Expected: compile FAIL — `Driver` undefined / `harness.DriveTmux` undefined.

- [ ] **Step 3: Implement.** `internal/harness/driver.go` with exactly the contract above (package `harness`, imports `context`; full doc comments in the spirit of harness.go's existing ones). Add to the `Harness` interface in `harness.go`:

```go
	// Driver returns how leo keeps a live session for this harness and
	// talks to it. Nil while the harness supports no interactive kinds
	// (SupportsKind gates every call site).
	Driver() SessionDriver
```

`internal/harness/claude/driver.go`:

```go
package claude

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// injectPromptFn is the seam driver tests replace; production uses
// tmux.InjectPrompt (readiness-probed paste + Enter).
var injectPromptFn = tmux.InjectPrompt

// SetInjectPromptForTest swaps the InjectPrompt seam and returns a restore
// func. Exported for _test files in this package only by convention.
func SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string) error) func() {
	prev := injectPromptFn
	injectPromptFn = fn
	return func() { injectPromptFn = prev }
}

// TmuxTUIDriver drives the interactive Claude Code TUI supervised in a leo
// tmux session. The supervisor owns the restart loop; this driver owns the
// claude-specific pane care and message delivery.
type TmuxTUIDriver struct{}

func (TmuxTUIDriver) Style() harness.DriveStyle { return harness.DriveTmux }

// Start is a no-op: the supervisor's tmux new-session already launched the
// TUI, and claude needs no post-launch arrangement beyond the pane care
// hooks the supervisor polls.
func (TmuxTUIDriver) Start(context.Context, harness.SessionHandle) error { return nil }

// Inject pastes msg into the live TUI. Delivery is asynchronous — the turn
// outcome lives in the pane / arrives via the Stop hook — so the Result is
// always nil.
func (TmuxTUIDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	tmuxPath, err := tmux.Locate()
	if err != nil {
		return nil, err
	}
	return nil, injectPromptFn(ctx, tmuxPath, h.TmuxSession, msg)
}

// Attach returns the plain tmux attach argv. The CLI's claude path keeps its
// richer behavior (display-popup nesting, -CC control mode) — this spec is
// the harness-neutral fallback shape.
func (TmuxTUIDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	tmuxPath, err := tmux.Locate()
	if err != nil {
		return harness.AttachSpec{}, err
	}
	argv := append([]string{tmuxPath}, tmux.Args("attach", "-t", tmux.Target(h.TmuxSession))...)
	return harness.AttachSpec{Argv: argv}, nil
}
```

Wire `Driver()`: claude returns `TmuxTUIDriver{}`; codex and opencode return `nil` with the comment `// Driver: no interactive kinds yet — the TurnDriver/ServerDriver lands in Plan-4 Task 5/6.` Update `registry_test.go`'s fake with `func (fakeHarness) Driver() harness.SessionDriver { return nil }` (match the existing fake's naming).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/harness/... && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/harness
git commit -m "feat(harness): SessionDriver contract + claude TmuxTUIDriver core"
```

---

### Task 2: Claude pure refactor — pane care + quick-exit ladder move behind the driver

**Files:**
- Modify: `internal/harness/claude/driver.go` (+ PaneCare + QuickExitRecovery implementations)
- Modify: `internal/harness/claude/driver_test.go` (ported tests)
- Modify: `internal/service/process.go` (delete moved helpers; consult driver)
- Modify: `internal/service/process_test.go` (move the pure-function tests out; keep supervisor-level tests)

**Interfaces:**
- Consumes: `harness.PaneCare`, `harness.QuickExitRecovery`, `harness.QuickExitAction` (Task 1).
- Produces: `claude.TmuxTUIDriver` implements both optional interfaces. The service package no longer owns `startupDialogKey`, `dialogDenyPattern`, `hasDialogChrome`, `hasSessionIDArg`, `hasResumeArg`, `stripResumeArg`, `convertSessionIDToResume` — they move (bodies unchanged) into the claude package as unexported helpers behind the two interface methods.

**Move map (byte-identical bodies, service → claude):**

| From `internal/service/process.go` | To `internal/harness/claude/driver.go` |
|---|---|
| `dialogDenyPattern` (line 800) | same name, unexported |
| `startupDialogKey` (lines 820–831) | body becomes `func (TmuxTUIDriver) PaneKey(pane string) string` |
| `hasDialogChrome` (lines 836–838) | same name, unexported (note: `internal/tmux` has its own copy — leave tmux's alone) |
| `stripResumeArg` / `hasResumeArg` / `hasSessionIDArg` / `convertSessionIDToResume` (lines 861–911) | same names, unexported |

`RecoverQuickExit` encodes today's `superviseProcess` switch (process.go lines 720–735) as data:

```go
// RecoverQuickExit implements the --session-id → --resume → fresh
// degradation ladder for quick exits (see the supervisor's doc comment).
func (TmuxTUIDriver) RecoverQuickExit(args []string) ([]string, harness.QuickExitAction) {
	switch {
	case hasSessionIDArg(args):
		return convertSessionIDToResume(args), harness.QuickExitRetryArgs
	case hasResumeArg(args):
		return stripResumeArg(args), harness.QuickExitClearAndNoResume
	default:
		return args, harness.QuickExitClearSession
	}
}
```

- [ ] **Step 1: Port the pure-function tests first (characterization).** Copy every existing test for `startupDialogKey`/dialog chrome and the arg-ladder helpers from `internal/service/process_test.go` into `internal/harness/claude/driver_test.go`, re-expressed against `TmuxTUIDriver{}.PaneKey(...)` and `TmuxTUIDriver{}.RecoverQuickExit(...)`. Every input/expectation pair is preserved verbatim. Add ladder-level cases:

```go
func TestRecoverQuickExitLadder(t *testing.T) {
	d := TmuxTUIDriver{}
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantAct  harness.QuickExitAction
	}{
		{"session-id converts to resume", []string{"--model", "opus", "--session-id", "abc"}, []string{"--model", "opus", "--resume", "abc"}, harness.QuickExitRetryArgs},
		{"resume strips and clears", []string{"--model", "opus", "--resume", "abc"}, []string{"--model", "opus"}, harness.QuickExitClearAndNoResume},
		{"plain args clear session", []string{"--model", "opus"}, []string{"--model", "opus"}, harness.QuickExitClearSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotAct := d.RecoverQuickExit(tt.args)
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) || gotAct != tt.wantAct {
				t.Errorf("got (%v, %d), want (%v, %d)", gotArgs, gotAct, tt.wantArgs, tt.wantAct)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify FAIL** — `go test -race ./internal/harness/claude/` → undefined `PaneKey`/`RecoverQuickExit`.

- [ ] **Step 3: Implement the move.** Move the helpers into `internal/harness/claude/driver.go` (bodies unchanged), add the two interface methods. In `internal/service/process.go`:
  - `waitForSessionEnd`'s dialog dismissal: replace the `startupDialogKey(...)` call inside `dismissStartupDialog` with a `paneKey func(string) string` obtained once: `superviseProcess` resolves `care, _ := drv.(harness.PaneCare)` from the process's driver and threads it (nil-safe: no PaneCare → skip dismissal entirely — for claude this is always non-nil, so behavior is unchanged).
  - The quick-exit switch (lines 720–735) becomes:

```go
	if elapsed < quickExitThreshold {
		newArgs, action := recoverQuickExit(drv, currentArgs)
		switch action {
		case harness.QuickExitRetryArgs:
			currentArgs = newArgs
			id.setArgs(currentArgs)
			fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs), retrying with --resume\n", name, harnessName, elapsed.Seconds())
		case harness.QuickExitClearAndNoResume:
			currentArgs = newArgs
			id.setArgs(currentArgs)
			clearProcessSession(homePath, name)
			markAgentNoResume(homePath, name)
			fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs), cleared stale session\n", name, harnessName, elapsed.Seconds())
		case harness.QuickExitClearSession:
			clearProcessSession(homePath, name)
			fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs)\n", name, harnessName, elapsed.Seconds())
		case harness.QuickExitNone:
			fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs)\n", name, harnessName, elapsed.Seconds())
		}
	}
```

  with

```go
// recoverQuickExit consults the driver's ladder when it has one; the default
// mirrors the historical behavior (clear the stored session, keep args).
func recoverQuickExit(drv harness.SessionDriver, args []string) ([]string, harness.QuickExitAction) {
	if r, ok := drv.(harness.QuickExitRecovery); ok {
		return r.RecoverQuickExit(args)
	}
	return args, harness.QuickExitClearSession
}
```

  **Log-line fidelity:** today's messages say `claude exited quickly …`. In this task `harnessName` is hardcoded `"claude"` for the process path (the ProcessSpec has no harness field until Task 3) so the strings stay byte-identical; Task 3 replaces it with the spec's harness name. `superviseProcess` gets the driver via `harness.Get("claude")` at the top (error impossible for the compiled-in adapter; on the defensive path log + fall back to nil driver semantics).
  - Keep `dismissStartupDialog`'s capture/send I/O in the service package — only the *decision* function moved.

- [ ] **Step 4: Run the full service + claude packages**

Run: `go test -race ./internal/service/ ./internal/harness/claude/ ./internal/tmux/`
Expected: PASS with zero assertion changes in surviving service tests (mechanical deletions of moved tests only).

- [ ] **Step 5: Full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/service internal/harness/claude
git commit -m "refactor(service): claude pane care + quick-exit ladder move behind SessionDriver"
```

---

### Task 3: Supervisor + spawn-path generalization (harness-aware specs, DriveTurns branch)

**Files:**
- Modify: `internal/service/process.go` (ProcessSpec.Harness; style branch in superviseProcess)
- Modify: `internal/service/idstore.go` (create: SessionIDStore impls over session.Store)
- Modify: `internal/service/agents.go` (RestoreAgents threads Harness)
- Modify: `internal/agent/types.go` (SpawnRequest.Harness), `internal/agent/manager.go` (spawn paths), `internal/agent/args.go` (type-switch)
- Modify: `internal/agentstore/store.go` (Record.Harness `json:"harness,omitempty"`)
- Modify: `internal/cli/service.go` (`buildProcessArgs` type-switch + ProcessSpec.Harness)
- Test: `internal/service/process_test.go`, `internal/agent/args_test.go`, `internal/agent/manager_test.go`, `internal/cli/service_test.go`

**Interfaces:**
- Consumes: `harness.SessionDriver.Style()`, `harness.SessionHandle`, `harness.SessionIDStore` (Task 1).
- Produces:
  - `service.ProcessSpec` gains `Harness string` (empty ⇒ `"claude"` everywhere it's read — restore records and old state files predate the field), `Kind harness.Kind` (builders set `KindProcess` for config processes, `KindAgent` for spawned/restored agents; empty ⇒ `KindProcess`), and `OpeningPrompt string` (non-claude only; claude keeps its prompt as the trailing positional in ClaudeArgs).
  - `agent.SpawnRequest` gains `Harness string` + `OpeningPrompt string`; `agentstore.Record` gains `Harness string`.
  - `service.newStoreIDs(homePath, key string) harness.SessionIDStore` — over `session.NewStore(homePath)` with keys `"process:<name>"` / `"session:<name>"`.
  - `agent` package: `newAgentIDs(homePath, name string) harness.SessionIDStore` — reads/writes `agentstore.Record.SessionID`.
  - `superviseProcess` behavior for `DriveTurns` drivers: register state `"running"`, **no tmux session**, call `drv.Start(ctx, handle)` in the supervise goroutine (errors logged, state `"stopped"` on failure), then block on `ctx.Done()`; no restart loop, no exit post-mortems.
  - `buildProcessArgs` / `BuildTemplateArgs` stop type-asserting to `claudeharness.Options` and instead type-switch `{claude, codex, opencode}` exactly like `internal/run/runner.go:965–1000` does (claude branch unchanged incl. `leomcp.MergeSystemPrompt`/`MCPConfigPath`/`LeoMCPArgs`; codex/opencode branches fill their `LeoMCP` bridge structs from the same web-token source the runner uses — for processes/agents that's `LEO_PROCESS_NAME=<name>`, `LEO_WEB_PORT`, `LEO_API_TOKEN` from the spawn env the supervisor already exports).
  - Claude-only bits gated on harness == claude: the `--session-id <fresh>` append in `Manager.spawnShared`/`spawnWorktree` (manager.go:196–197, 303–304), the `session.LatestSession` jsonl scan in `Manager.Resume` (manager.go:560), and `resolveSessionState`'s stale-resume mtime check in `internal/cli/service.go`.
  - **Codex/opencode `Args()` for interactive kinds still error in this task** (their drivers don't exist yet); the new type-switch branches are unit-tested via table tests that stop at the options-fill stage. Config validation still rejects the kinds, so no user-visible change.

- [ ] **Step 1: Characterization tests first.** Add/verify tests that pin today's claude behavior through the new plumbing: `buildProcessArgs` golden argv for a representative process config (already exists — extend with an assertion that `ProcessSpec.Harness == "claude"`), `BuildTemplateArgs` golden argv (exists), `SpawnAgent → agentstore.Record.Harness == "claude"`. Run them against the pre-task code where they can compile; where they reference the new field they are the RED step.

- [ ] **Step 2: Implement** as specified in Interfaces. Key code:

```go
// driverFor resolves a spec's session driver. Empty harness means claude
// (records/state written before the field existed).
func driverFor(harnessName string) harness.SessionDriver {
	if harnessName == "" {
		harnessName = "claude"
	}
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil // unreachable for validated configs; callers nil-check
	}
	return h.Driver()
}

// handleForSpec builds the SessionHandle superviseProcess hands to drivers.
func handleForSpec(spec ProcessSpec, id *procIdentity, homePath string) harness.SessionHandle {
	kind := spec.Kind
	if kind == "" {
		kind = harness.KindProcess
	}
	return harness.SessionHandle{
		Kind:          kind,
		Name:          id.Name(),
		TmuxSession:   id.SessionName(),
		Workspace:     spec.WorkDir,
		HomePath:      homePath,
		Env:           spec.Env,
		TurnArgs:      id.Args(),
		OpeningPrompt: spec.OpeningPrompt,
		IDs:           agentOrProcessIDs(homePath, id.Name()),
	}
}
```

  The style branch at the top of `superviseProcess` (after `sv.initState`):

```go
	drv := driverFor(spec.Harness)
	if drv != nil && drv.Style() == harness.DriveTurns {
		superviseTurnBased(ctx, spec, homePath, sv, id, drv)
		return
	}
```

```go
// superviseTurnBased registers a turn-driven session (codex): no resident
// process, no restart loop. Start runs the opening turn (if any) and
// records the thread id; the session then idles until Inject calls arrive
// through the daemon/web dispatch paths.
func superviseTurnBased(ctx context.Context, spec ProcessSpec, homePath string, sv *Supervisor, id *procIdentity, drv harness.SessionDriver) {
	name := id.Name()
	sv.setState(name, "running")
	if err := drv.Start(ctx, handleForSpec(spec, id, homePath)); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] driver start: %v\n", name, err)
		sv.setState(name, "stopped")
		return
	}
	<-ctx.Done()
	sv.setState(name, "stopped")
}
```

  **DriveTmux drivers also get `Start`** — in `superviseProcess`, immediately after a successful `tmux new-session` (and on the adopt path), launch it without blocking the loop, and make the opening prompt one-shot across restart iterations:

```go
	startHandle := handleForSpec(spec, id, homePath)
	startHandle.OpeningPrompt = openingPrompt // local var; cleared after first use, like `adopt`
	openingPrompt = ""
	go func(h harness.SessionHandle) {
		if err := drv.Start(ctx, h); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[%s] driver start: %v\n", h.Name, err)
		}
	}(startHandle)
```

  Claude's `Start` is a no-op, so this is behavior-neutral for every existing config (assert that in a characterization test: supervise a claude spec through the seams and confirm zero new tmux/exec calls). `agentOrProcessIDs` picks the agentstore-backed store when an agentstore record exists for the name, else the session-store `"process:"` key — one small helper, unit-tested.

- [ ] **Step 3: Tests green** — `go test -race ./internal/service/ ./internal/agent/ ./internal/cli/ ./internal/agentstore/` PASS; claude argv goldens byte-identical.

- [ ] **Step 4: Full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add -A internal
git commit -m "feat(service,agent): harness-aware specs + turn-based supervision branch"
```

---

### Task 4: Injection, attach, logs, sweep dispatch through the driver

**Files:**
- Modify: `internal/web/handlers.go` (`handleProcessMessage` non-claude branch), `internal/web/web.go` (driver resolver wiring)
- Modify: `internal/daemon/session_router.go` (injector returns `*harness.Result`; sync completion), `internal/daemon/server.go` (SetInjector signature)
- Modify: `internal/service/process.go` or wiring site (harness-aware injector closure passed to daemon)
- Modify: `internal/cli/attach.go` + `internal/cli/tmux.go` (non-claude attach via AttachSpec)
- Modify: `internal/agent/manager.go` (`Logs` for non-tmux drivers → HistoryPath tail; `Resume`/suspend gating)
- Modify: `internal/service/sweep.go` (skip non-claude agents)
- Test: `internal/web/handlers_test.go`, `internal/daemon/session_router_test.go`, `internal/cli/attach_test.go` (or tmux_test.go), `internal/service/sweep_test.go`, e2e `persistent_helpers_test.go` (injector signature)

**Interfaces:**
- Consumes: `harness.SessionDriver`, `AttachSpec`, `SessionHandle` (Task 1), `Record.Harness`/`ProcessSpec.Harness` (Task 3).
- Produces:
  - `daemon` injector type becomes `func(tmuxSession, prompt string) (*harness.Result, error)`; the pump treats a non-nil Result as **synchronous completion**: mark the invocation completed immediately with `Result.Text` (and failed when `Result.IsError`), skipping the await-Report window. Nil Result keeps today's async wait byte-identical.
  - `web` resolves a message target to `(harnessName, SessionHandle)` and routes **non-claude** targets to `driver.Inject`. **Claude targets keep the existing fast path and suspended-resume path untouched** (characterization). Handle construction:
    - *Agents:* from the agentstore record — `Harness`, `TurnArgs = rec.ClaudeArgs` (for codex that IS the turn prefix, stored at spawn), `Workspace`, `Env`, `TmuxSession = agent.SessionName(name)`, `IDs` = the agentstore-backed store.
    - *Processes:* the resolver is a closure constructed at service boot (where `[]ProcessSpec` exists) and wired into web the same way `injectPrompt` is today (`internal/web/web.go:186` pattern): `s.resolveHandle func(name) (harnessName string, h harness.SessionHandle, ok bool)`. Nil/`!ok` ⇒ claude behavior (today's path). The closure reads live argv from the supervisor's `procIdentity` (a rename mid-flight is absorbed the same way `waitForSessionEnd` absorbs it).
  - `leo agent attach` / `leo attach`: claude targets keep the existing `attachTmuxSession` flow byte-identical; non-claude targets call `driver.Attach` — `Argv` non-nil → local `agentSyscallExec` (or ssh -tt for remote hosts, argv shell-quoted per memory `project_ssh_argv_flatten_remote_shell`), `Argv` nil → print the tail of `HistoryPath` with a one-line note that this harness has no live attach.
  - `Manager.Logs`: when the agent's harness driver is `DriveTurns`, return the tail of the driver's `AttachSpec.HistoryPath` instead of tmux capture-pane.
  - `sweepIdleAgents` skips records whose `Harness` is neither empty nor `"claude"` (one guard + test).
  - Suspended-agent auto-resume on message (web resume path) stays claude-only: non-claude agents never suspend, and the handler returns the existing "not running" error shape if it somehow meets one.

- [ ] **Step 1: RED — router sync-completion test.** In `internal/daemon/session_router_test.go`, add: enqueue one invocation with an injector stub returning `&harness.Result{Text: "done", SessionID: "t1"}`; assert the invocation completes without any `Report` call, result text `"done"`, and the stored-session update callback got `"t1"`. Add the mirror case `IsError: true` → invocation marked failed. Existing async tests keep passing with injectors returning `(nil, nil)`.

- [ ] **Step 2: RED — web dispatch test.** Fake driver registered under a fake harness in the test (or stub the resolver seam): message to a non-claude agent lands in `driver.Inject` with the right `SessionHandle` (name, tmux session, workspace) and does NOT touch the tmux fast path (assert the `execCommand` seam saw zero tmux calls).

- [ ] **Step 3: Implement.** Router: change `injectFn` to the new signature; in `pump`, after a successful inject with non-nil result, call the same completion bookkeeping the Report path uses (factor the small shared helper rather than duplicating). Wire the harness-aware injector closure where `SetInjector` is called today (`internal/daemon/server.go:79` — move construction to the service boot so it can reach config/drivers; daemon keeps only the setter). Update the e2e mock injector signature in the same commit (`e2e/persistent_helpers_test.go`) — return `(nil, nil)` to preserve its behavior.

- [ ] **Step 4: Implement attach/logs/sweep** per Interfaces. Attach argv execution reuses `agentSyscallExec` exactly like `attachTmuxSession`'s outside-tmux branch (`internal/cli/tmux.go:120–123`).

- [ ] **Step 5: Green + full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add -A internal e2e
git commit -m "feat(web,daemon,cli): route non-claude injection/attach/logs through SessionDriver"
```

---

### Task 5: codex TurnDriver

**Files:**
- Create: `internal/harness/codex/driver.go`
- Create: `internal/harness/codex/driver_test.go`
- Modify: `internal/harness/codex/codex.go` (`Driver()`, `SupportsKind` {task, process, agent}, `Args` interactive kinds = turn prefix)
- Modify: `internal/harness/codex/codex_test.go`
- Modify: `internal/config/config.go` (drop the process/template "not supported yet" validation errors for codex — they now pass `SupportsKind`; **sessions + persistent tasks stay rejected until Task 7**)
- Modify: `internal/config/config_test.go`
- Test fixtures: reuse `internal/harness/codex/testdata/{fresh,resume}.jsonl`

**Interfaces:**
- Consumes: contract from Task 1; `TurnArgs` threading from Task 3; dispatch from Task 4.
- Produces:
  - `codex.Args(spec)` for `KindProcess`/`KindAgent`: returns the **per-turn argv prefix** — `exec --json --skip-git-repo-check [-m model] [--sandbox s] [-c mcp…]` — i.e. today's `KindTask` argv *minus* `SessionArgs` and the trailing prompt. `KindSession` keeps its rejection until Task 7. `SessionPinned` still rejected.
  - `TurnDriver`:

```go
// TurnDriver drives codex turn-per-process: no resident process; each
// injected message spawns `codex exec … resume <thread-id> <msg>` in the
// workspace and blocks until it exits. Turns are serialized per session.
type TurnDriver struct{}

func (TurnDriver) Style() harness.DriveStyle { return harness.DriveTurns }
```

  - `Start`: if `h.OpeningPrompt != ""` and `h.IDs.Get() == ""`, run the opening turn **synchronously in the supervise goroutine** (it is already async relative to SpawnAgent): argv = `h.TurnArgs + [prompt]`, `Dir = h.Workspace`, env = process env + `h.Env`; parse the combined stdout with `Codex{}.ParseEvents`; `h.IDs.Set(res.SessionID)`; append the turn to the transcript. If a thread id is already stored, Start is a no-op ("restart is bookkeeping").
  - `Inject`: per-`TmuxSession` mutex (package-level `sync.Map` of `*sync.Mutex`); argv = `h.TurnArgs + ["resume", id] + [msg]` when `h.IDs.Get() != ""`, else `h.TurnArgs + [msg]`; run via the package seam `execCommand` (`exec.CommandContext` default, `Dir = h.Workspace`); on exit parse `ParseEvents`; persist `res.SessionID` when non-empty; append `> msg` / result text to the transcript; return `&res, nil`.
  - **Stale-thread fallback (one-step ladder):** resume turn exits non-zero with empty stdout → `h.IDs.Clear()` and retry once as a fresh turn with the same msg (mirrors claude's poisoned-session recovery; the stderr shape is the Plan-3-verified `no rollout found`). Retry at most once per Inject.
  - Transcript: `filepath.Join(h.HomePath, "state", "transcripts", h.TmuxSession+".log")` (mkdir 0750, append 0600), plain text: `--- <RFC3339> user\n<msg>\n--- <RFC3339> codex\n<text>\n`. `Attach` returns `AttachSpec{HistoryPath: <that path>}` (nil Argv).
  - `AbortTurn`: cancel func registry keyed by `TmuxSession`, set around the in-flight `exec.CommandContext`; no-op when idle.
  - Config validation: `processes.*`/`templates.*` with `harness: codex` now pass; delete the two corresponding error strings from `internal/config/config.go:602–604, 659–661` (they're behind `SupportsKind`, so the flip alone removes them — delete any dead test expecting them and add the passing-case tests).

- [ ] **Step 1: RED — driver tests** (all through the `execCommand` seam; fixture bytes as stdout):

```go
func TestTurnDriverInjectFreshRecordsThreadID(t *testing.T)   // no stored id → argv has no "resume"; ParseEvents on testdata/fresh.jsonl → IDs.Set("<thread-id from fixture>"); Result.Text matches fixture
func TestTurnDriverInjectResumeArgvOrder(t *testing.T)        // stored id "th_1" → argv == TurnArgs + ["resume","th_1","hi"] exactly (strings.Join \x00 compare)
func TestTurnDriverStaleResumeFallsBackFresh(t *testing.T)    // first exec: exit 1, empty stdout → second exec without "resume"; IDs cleared then re-set
func TestTurnDriverSerializesPerSession(t *testing.T)         // two concurrent Injects on one handle → seam records non-overlapping execution windows
func TestTurnDriverStartRunsOpeningPromptOnce(t *testing.T)   // OpeningPrompt + empty IDs → one exec with prompt; second Start with stored id → zero execs
func TestTurnDriverTranscriptAppends(t *testing.T)            // transcript file contains both user msg and result text
func TestCodexArgsInteractiveKindsRenderTurnPrefix(t *testing.T) // KindProcess/KindAgent golden: {"exec","--json","--skip-git-repo-check","--model","gpt-5.3-codex","-c",...} with no prompt, no resume
```

- [ ] **Step 2: Run RED** — `go test -race ./internal/harness/codex/` → undefined symbols.

- [ ] **Step 3: Implement** driver.go + codex.go changes per Interfaces. Core of `internal/harness/codex/driver.go` (seam `var execCommand = exec.CommandContext`):

```go
// turnMu serializes turns per session: concurrent Injects into the same
// codex thread would interleave rollout writes.
var turnMu sync.Map // TmuxSession → *sync.Mutex

// abortMu/aborts track the in-flight turn's cancel func per session so
// AbortTurn can kill it.
var aborts sync.Map // TmuxSession → context.CancelFunc

func lockFor(key string) *sync.Mutex {
	mu, _ := turnMu.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (d TurnDriver) Start(ctx context.Context, h harness.SessionHandle) error {
	if h.OpeningPrompt == "" || h.IDs.Get() != "" {
		return nil // restart is bookkeeping; an existing thread just resumes on the next message
	}
	_, err := d.runTurn(ctx, h, h.OpeningPrompt)
	return err
}

func (d TurnDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	res, err := d.runTurn(ctx, h, msg)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runTurn spawns one codex exec turn and blocks until it exits. A resume
// against a vanished thread ("no rollout found") clears the stored id and
// retries once as a fresh turn.
func (d TurnDriver) runTurn(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	mu := lockFor(h.TmuxSession)
	mu.Lock()
	defer mu.Unlock()

	run := func(resumeID string) (harness.Result, int, error) {
		turnCtx, cancel := context.WithCancel(ctx)
		aborts.Store(h.TmuxSession, cancel)
		defer func() { aborts.Delete(h.TmuxSession); cancel() }()

		args := append([]string{}, h.TurnArgs...)
		if resumeID != "" {
			args = append(args, "resume", resumeID)
		}
		args = append(args, msg)
		cmd := execCommand(turnCtx, Codex{}.Binary(), args...)
		cmd.Dir = h.Workspace
		cmd.Env = append(os.Environ(), envSlice(h.Env)...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		runErr := cmd.Run()
		res, perr := Codex{}.ParseEvents(&stdout)
		if perr != nil {
			return harness.Result{}, -1, perr
		}
		exit := 0
		if runErr != nil {
			exit = 1 // detail-precision not needed: any failure with empty output is the stale-thread shape
		}
		return res, exit, nil
	}

	id := h.IDs.Get()
	res, exit, err := run(id)
	if err != nil {
		return nil, err
	}
	if exit != 0 && id != "" && res.SessionID == "" && res.Text == "" {
		// Stale thread: clear and retry once fresh (one-step ladder).
		h.IDs.Clear()
		res, exit, err = run("")
		if err != nil {
			return nil, err
		}
	}
	if res.SessionID != "" {
		h.IDs.Set(res.SessionID)
	}
	if exit != 0 {
		res.IsError = true
	}
	appendTranscript(h, msg, res)
	return &res, nil
}

func (TurnDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{HistoryPath: transcriptPath(h)}, nil
}

func (TurnDriver) AbortTurn(h harness.SessionHandle) error {
	if c, ok := aborts.Load(h.TmuxSession); ok {
		c.(context.CancelFunc)()
	}
	return nil
}

func transcriptPath(h harness.SessionHandle) string {
	return filepath.Join(h.HomePath, "state", "transcripts", h.TmuxSession+".log")
}
```

  (`appendTranscript` mkdirs 0750, appends 0600, format per Interfaces; `envSlice` renders the map as `K=V` pairs — small helpers with their own table tests. Note `cmd.Run()` with a cancelled context kills the child — that's what makes `AbortTurn` and the Task-7 timeout work.)

- [ ] **Step 4: Config flip.** Table-driven validation tests: `processes.p.harness: codex` valid; `templates.t.harness: codex` valid; `sessions.s.harness: codex` still errors with the existing string; persistent task on codex still errors. Adjust `TestNonClaudeValidationErrors` expectations in e2e in the same commit.

- [ ] **Step 5: Green + full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add -A internal e2e
git commit -m "feat(codex): TurnDriver — processes + ephemeral agents on codex"
```

---

### Task 6: opencode ServerDriver

**Files:**
- Create: `internal/harness/opencode/driver.go`
- Create: `internal/harness/opencode/driver_test.go`
- Create: `internal/harness/opencode/serverstate.go` (+ `serverstate_test.go`)
- Modify: `internal/harness/opencode/opencode.go` (`Driver()`, `SupportsKind` {task, process, agent}, `Args` interactive kinds = serve argv)
- Modify: `internal/harness/opencode/options.go` (runtime-only `ServerPort int`, `ServerPassword string`; `Env` adds `OPENCODE_SERVER_PASSWORD`)
- Modify: `internal/cli/service.go` + `internal/agent/args.go` (provision server state before `Args` when harness == opencode)
- Modify: `internal/config/config.go` + tests (drop process/template rejections for opencode; sessions/persistent stay until Task 7)
- Test fixtures: `internal/harness/opencode/testdata/{attach_fresh.jsonl,session_list.json}` (Task 0)

**Interfaces:**
- Consumes: contract (Task 1), spawn threading (Task 3), dispatch (Task 4).
- Produces:
  - `opencode.ServerState{Port int; Password string; Model string}` persisted at `<home>/state/opencode/<tmux-session>.json` (0600, dir 0750). `EnsureServerState(homePath, tmuxSession, model string) (ServerState, error)`: reuse the file when present (port stability across restarts — attach URLs keep working); otherwise allocate a free port (`net.Listen("tcp4", "127.0.0.1:0")`, close, take port), generate a 32-hex-char password (`crypto/rand`), write, return. `LoadServerState(homePath, tmuxSession)` for the driver/CLI read side.
  - `opencode.Args` for `KindProcess`/`KindAgent`: `{"serve", "--port", strconv.Itoa(opts.ServerPort), "--hostname", "127.0.0.1"}` (model is per-run, not per-server). Missing `ServerPort` (0) → error `opencode: internal error: server port not provisioned` (programmer error, never user-visible).
  - `opencode.Env` additionally returns `OPENCODE_SERVER_PASSWORD` when `opts.ServerPassword != ""` (alongside the existing `OPENCODE_CONFIG_CONTENT`; both ride the tmux shell exports via `ProcessSpec.Env` — the spawn builders merge them, caller env still wins on collision, matching the runner's Plan-3 semantics).
  - `ServerDriver`:

```go
// ServerDriver drives opencode's headless server: the supervised resident
// process is `opencode serve`; messages go in via `opencode run --attach`
// (which blocks until the turn completes — attach-mode event forwarding is
// lossy, so process exit, not step_finish, is the turn-end signal); attach
// is opencode's own TUI client.
type ServerDriver struct{}

func (ServerDriver) Style() harness.DriveStyle { return harness.DriveTmux }
```

  - `Start`: load ServerState; poll `GET http://127.0.0.1:<port>/global/health` (500ms interval, 60s budget, ctx-aware — mirrors the claude readiness-probe budget) until `{"healthy":true}`; then if `h.OpeningPrompt != "" && h.IDs.Get() == ""` run one Inject with it in a goroutine (log failures; the IDs-empty guard makes restarts prompt-safe — a serve crash never re-sends the opening prompt).
  - `Inject`: argv `{"run", "--attach", "http://127.0.0.1:<port>", "--format", "json", "--dir", h.Workspace}` + `["--model", state.Model]` when set + `["-s", id]` when `h.IDs.Get() != ""` + `[msg]`; env: parent + `OPENCODE_SERVER_PASSWORD=<state.Password>`; `Dir = h.Workspace`; run via package `execCommand` seam; on exit `ParseEvents` (EOF-tolerant, Plan 3) → **session-id capture ladder:** (1) `res.SessionID` from any event; (2) else `opencode session list --format json` in `h.Workspace`, filter `directory == h.Workspace`, newest `created` → id; persist via `h.IDs.Set`. **Stale-session fallback:** non-zero exit + empty stdout with a stored id → `h.IDs.Clear()`, retry once without `-s`. Return `&res, nil`.
  - `Attach`: `AttachSpec{Argv: {"opencode", "attach", "http://127.0.0.1:<port>", "--dir", h.Workspace, "-p", state.Password} + ["-s", id] when stored}`. (Password lands in argv for attach only — interactive, user-invoked, localhost; documented in Task 9. Inject keeps it in env.)
  - `RecoverQuickExit`: `return args, harness.QuickExitNone` — a crashed serve is NOT a poisoned conversation; the stored `ses_…` id must survive restarts.
  - `AbortTurn`: same cancel-registry pattern as codex.
  - Spawn builders (`internal/cli/service.go` buildProcessArgs, `internal/agent/args.go` BuildTemplateArgs): in the opencode branch of the Task-3 type-switch, call `EnsureServerState(homePath, tmuxSession, model)` and fill `opts.ServerPort/ServerPassword` before `h.Args(spec)`; also fill `opts.LeoMCP` (bridge command `leo mcp-server`, env map = the same `LEO_PROCESS_NAME`/`LEO_WEB_PORT`/`LEO_API_TOKEN` values, inline — verified shape from Plan 3).

- [ ] **Step 1: RED — driver + state tests:**

```go
func TestEnsureServerStateAllocatesAndPersists(t *testing.T)   // fresh: nonzero port, 32-hex-char password, file mode 0600; second call returns identical state
func TestServeArgsRenderServeCommand(t *testing.T)             // KindProcess golden: {"serve","--port","45991","--hostname","127.0.0.1"}
func TestServeArgsWithoutProvisionError(t *testing.T)          // ServerPort==0 → exact error string
func TestServerDriverStartWaitsForHealth(t *testing.T)         // httptest server flips healthy on 3rd poll → Start returns nil; never-healthy + tiny budget → error
func TestServerDriverInjectArgvAndEnv(t *testing.T)            // stored id → argv exact incl. --dir/-s/msg; env contains OPENCODE_SERVER_PASSWORD
func TestServerDriverInjectSessionIDFromStream(t *testing.T)   // stdout = testdata/attach_fresh.jsonl → IDs.Set("ses_0ae242650ffeKkgOmScky8of5r")
func TestServerDriverInjectSessionIDFallbackToList(t *testing.T) // stdout empty → second exec = session list; parses testdata/session_list.json, filters directory, sets id
func TestServerDriverStaleSessionRetriesFresh(t *testing.T)    // exit 1 + empty stdout with stored id → cleared + one retry without -s
func TestServerDriverQuickExitKeepsSession(t *testing.T)       // RecoverQuickExit returns (args, QuickExitNone)
```

  (Health polling: point the driver's base URL at `httptest.Server` via a `healthURLFn`/port-override seam — keep it simple: the driver builds URLs from the port; tests write a ServerState with the httptest port.)

- [ ] **Step 2: Run RED** — `go test -race ./internal/harness/opencode/` → undefined symbols.

- [ ] **Step 3: Implement.** Core of `ServerDriver.Inject` (same seam/serialization/abort pattern as codex — package `var execCommand = exec.CommandContext`, per-`TmuxSession` mutex + cancel registry):

```go
func (d ServerDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	mu := lockFor(h.TmuxSession)
	mu.Lock()
	defer mu.Unlock()

	state, err := LoadServerState(h.HomePath, h.TmuxSession)
	if err != nil {
		return nil, fmt.Errorf("opencode: loading server state: %w", err)
	}

	run := func(sessionID string) (harness.Result, int, error) {
		turnCtx, cancel := context.WithCancel(ctx)
		aborts.Store(h.TmuxSession, cancel)
		defer func() { aborts.Delete(h.TmuxSession); cancel() }()

		args := []string{"run", "--attach", state.URL(), "--format", "json", "--dir", h.Workspace}
		if state.Model != "" {
			args = append(args, "--model", state.Model)
		}
		if sessionID != "" {
			args = append(args, "-s", sessionID)
		}
		args = append(args, msg)
		cmd := execCommand(turnCtx, Opencode{}.Binary(), args...)
		cmd.Dir = h.Workspace
		cmd.Env = append(os.Environ(), "OPENCODE_SERVER_PASSWORD="+state.Password)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard // stderr carries ANSI-coded errors; exit code + empty stdout is the signal
		runErr := cmd.Run()
		res, perr := Opencode{}.ParseEvents(&stdout)
		if perr != nil {
			return harness.Result{}, -1, perr
		}
		exit := 0
		if runErr != nil {
			exit = 1
		}
		return res, exit, nil
	}

	id := h.IDs.Get()
	res, exit, err := run(id)
	if err != nil {
		return nil, err
	}
	if exit != 0 && id != "" && res.SessionID == "" && res.Text == "" {
		h.IDs.Clear() // "Session not found" shape: retry once fresh
		res, exit, err = run("")
		if err != nil {
			return nil, err
		}
	}
	if res.SessionID == "" && exit == 0 {
		res.SessionID = latestSessionIDForDir(ctx, h.Workspace) // `opencode session list --format json` fallback (lossy attach stream)
	}
	if res.SessionID != "" {
		h.IDs.Set(res.SessionID)
	}
	if exit != 0 {
		res.IsError = true
	}
	return &res, nil
}
```

  (`ServerState.URL()` returns `"http://127.0.0.1:" + strconv.Itoa(s.Port)`. `latestSessionIDForDir` runs `session list --format json` via the same seam, unmarshals `[]struct{ ID string `json:"id"`; Created int64 `json:"created"`; Directory string `json:"directory"` }`, filters `Directory == workspace`, returns the newest `Created`'s ID, `""` on any failure — a missing id is tolerable, the next turn's fallback retries.)

- [ ] **Step 4: Config flip** — mirror Task 5's validation-test reshaping for opencode (`processes.*`/`templates.*` pass; `sessions.*` + persistent tasks still rejected until Task 7).

- [ ] **Step 5: Green + full gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add -A internal e2e
git commit -m "feat(opencode): ServerDriver — processes + ephemeral agents on opencode serve"
```

---

### Task 7: Persistent sessions (KindSession) on codex + opencode

**Files:**
- Modify: `internal/service/session.go` (SessionSpec.Harness + HarnessOptions decode switch; SuperviseSession style branch)
- Modify: `internal/service/session_test.go`
- Modify: `internal/run/persistent.go` (prompt wrapping per harness)
- Modify: `internal/run/persistent_test.go`
- Modify: `internal/harness/codex/codex.go` + `internal/harness/opencode/opencode.go` (SupportsKind adds `KindSession`; codex `Args(KindSession)` = turn prefix, opencode = serve argv)
- Modify: `internal/config/config.go` + `internal/config/config_test.go` (drop sessions.* + persistent-task rejections)
- Modify: `internal/daemon/session_router.go` (+test) only if the Task-4 sync path needs the timeout wrap below
- Test: e2e `persistent_*_test.go` expectations for the injector signature (already updated Task 4)

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `SessionSpec` gains `Harness string` and `HarnessOptions map[string]any` (raw, for non-claude decode); `SessionSpecsFromConfig` resolves `cfg.SessionHarness(sc)` and, for non-claude, skips the claude options decode (preserving the implicit-session no-cascade quirk for claude only, unchanged).
  - `SuperviseSession`: claude path byte-identical (LoopSpec, Stop hook, OnQuickExit). codex: no tmux, no Stop hook — register nothing to supervise; the "session" exists as stored state + driver dispatch (the daemon injector reaches it by its `leo-session-<name>` routing key). opencode: LoopSpec whose ShellCmd wraps `opencode serve` argv + env exports (reusing `buildShell`'s export mechanics with the opencode Env map; no `EnsureLeoStopHook` — that's claude-only), `OnQuickExit` = no-op (QuickExitNone semantics).
  - `runPersistent` prompt wrapping: claude keeps `wrapPromptForPersistent` (invocation marker + channel-delivery preamble) byte-identical; non-claude sessions enqueue the **bare assembled prompt** plus a short delivery note pointing at `leo_send_message` when the task declares no channels (channels are invalid on non-claude anyway) — no invocation marker (completion is synchronous via the injector's Result).
  - Session handles for the injector: at boot, the harness-aware injector closure (Task 4) gains a map `tmuxSession → (harnessName, harness.SessionHandle)` built from the resolved `[]SessionSpec` — for non-claude sessions, `TurnArgs` comes from `h.Args(LaunchSpec{Kind: KindSession, …})` (codex: turn prefix; opencode: unused), `IDs` = `newStoreIDs(homePath, "session:"+name)`, `TmuxSession` = `SessionTmuxName(name)`, and (opencode) `EnsureServerState` provisioning happens in `SessionSpecsFromConfig`'s successor path exactly as the process builders do it.
  - Timeout semantics for sync turns: the harness-aware injector wraps `driver.Inject` in `context.WithTimeout(task timeout)` supplied through the enqueue request — the pump already carries `Timeout`; thread it into the injector call so a hung turn is killed (CommandContext) and surfaces as a failed invocation. Abort (`leo_interrupt` path): router's aborter for non-claude sessions calls the driver's `AbortTurn`.
  - Config: `sessions.s.harness: codex|opencode` and persistent tasks on both now validate; session-name/topology rules unchanged.

- [ ] **Step 1: RED tests:** `SessionSpecsFromConfig` with a codex session yields `Harness: "codex"` without decoding claude options; persistent enqueue for an opencode session carries the bare prompt (no `leo:invocation=` marker — assert absence); router test: sync-result invocation honors the timeout (stub injector sleeps past a tiny timeout → invocation failed).
- [ ] **Step 2: RED run**, **Step 3: implement**, **Step 4: validation flips + e2e expectation updates**, **Step 5: gates + commit**

```bash
go test -race ./... && make lint && make e2e
git add -A internal e2e
git commit -m "feat(sessions): codex + opencode persistent sessions via session drivers"
```

---

### Task 8: e2e — driver flows on fakes + gated real-binary smoke

**Files:**
- Modify: `e2e/fakecodex/main.go` (resume-turn assertions already work; add `FAKECODEX_SCENARIO=stale_resume` → exit 1/empty stdout on `resume`, success on fresh)
- Modify: `e2e/fakeopencode/main.go` (add `serve` mode: real HTTP listener on `--port` serving `/global/health`; add `run --attach` mode emitting the lossy single-`step_start` stream then exit 0; add `session list --format json` mode)
- Create: `e2e/driver_test.go` (build tag `e2e`)
- Modify: `e2e/harness_task_test.go` (real-smoke extension)

**Interfaces:**
- Consumes: the whole feature. Real tmux is NOT exercised in e2e (established constraint) — driver flows are covered at the seams e2e already uses (daemon boot + agent spawn handlers + fake binaries on PATH).
- Produces test coverage:
  - **codex agent flow:** spawn a codex-template agent via the daemon/manager path with an opening prompt → assert fakecodex saw one fresh `exec --json` turn with the prompt, agentstore recorded the fake thread id and `harness: "codex"`; send a message via the web message endpoint → fakecodex saw `resume <id> <msg>` argv (exact order via `\x00` join); `stale_resume` scenario → second call without `resume` and store updated.
  - **opencode agent flow:** spawn against fakeopencode `serve` (the supervisor needs tmux — NOT available; so this flow drives the driver directly at unit level and e2e covers: provisioning file created with port/password; message endpoint → fakeopencode `run --attach` argv incl. `--dir`, `-s`, password env from `FAKEOPENCODE_ENVLOG`; session-list fallback scenario).
  - **persistent session sync completion:** daemon enqueue on a codex session with the real (non-mock) harness-aware injector but fake binary → invocation completes with the fake's text, history records it, no Report call needed.
  - **Real-binary smoke (`LEO_E2E_REAL_HARNESSES=1`, orchestrator runs before PR):** codex — fresh turn then resume turn through the TurnDriver against real codex (models unpinned per Plan-3 lesson); opencode — real `opencode serve` provisioned by `EnsureServerState`, health-wait, one injected turn, session id persisted, `session list` fallback exercised, server killed.
- [ ] **Step 1:** Write the fake extensions + RED e2e tests; **Step 2:** implement until green: `make e2e`; **Step 3:** full gates + commit

```bash
go test -race ./... && make lint && make e2e
git add e2e
git commit -m "test(e2e): codex/opencode session-driver flows on fakes + gated real smoke"
```

---

### Task 9: Docs

**Files:**
- Modify: `docs/configuration/harnesses.md` (support matrix: all three harnesses × {tasks, processes, agents, sessions}; driver semantics section — codex turn-driven [no live attach, history instead; restart is bookkeeping], opencode server-backed [port/password state under `state/opencode/`, attach = opencode TUI, localhost + basic-auth]; MCP bridge note fixed: gate is web UI enabled **and readable `state/api.token`** — the deferred Plan-3 nit)
- Modify: `docs/configuration/persistent-tasks.md` (harness column/notes: non-claude sessions complete synchronously, no Stop hook, no invocation marker)
- Modify: `docs/configuration/config-reference.md` (SupportsKind matrix update; `state/opencode/<session>.json` + `state/transcripts/` entries)
- Modify: `internal/templates/` embedded docs ONLY if they repeat the "tasks only" claim (grep `session drivers land`; scrub any hit — Plan-2 lesson: embedded skill config-reference needed the same scrub)

- [ ] **Step 1:** Update docs; every CLI claim must match a Verified-facts line or live code (byte-check flags against `Args` implementations).
- [ ] **Step 2:** `grep -rn "not supported yet\|only scheduled tasks" docs/ internal/templates/` → zero stale hits for process/agent/session kinds.
- [ ] **Step 3:** Gates + commit

```bash
go test -race ./... && make lint && make e2e
git add docs internal/templates
git commit -m "docs: harness support matrix + session-driver semantics"
```
