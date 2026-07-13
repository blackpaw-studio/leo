# Uniform tmux-TUI Drivers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every harness (claude, codex, opencode) drives its live sessions as a persistent interactive TUI inside `leo-<name>` tmux — one shared injection machine, one attach UX, per-harness profiles.

**Architecture:** Claude's tmux machinery is promoted: `tmux.InjectPrompt` gains a per-harness input-marker profile; a new shared `tmuxtui.Driver` (parameterized by probe profile, dialog policy, pre-launch hook, session-args refresher, and session-id discovery) replaces codex's `TurnDriver` and opencode's `ServerDriver`. The supervise loop stays generic and gains two optional driver capabilities (PreLauncher, SessionArgsRefresher). Attach collapses to a plain tmux attach for all three harnesses.

**Tech Stack:** Go 1.x, tmux, cobra; spec: `docs/superpowers/specs/2026-07-12-uniform-tui-drivers-design.md`.

## Global Constraints

- Gates on EVERY task commit: `go test -race ./...`, `make lint`, `make e2e` (build-tagged; `go test ./...` does NOT run it), `golangci-lint run ./...` (v2.12.2, brew), `~/go/bin/gosec -exclude=G104,G204,G304,G306,G602,G702,G703,G704 -quiet ./...` — all clean.
- CI runners have NO tmux and NO harness binaries: tests must stub the existing seams (`injectPromptFn`, `locateTmuxFn`, `execCommand`, `lookPath`, `loopExecCommand`, `supervisedExecFn`) — never invoke real binaries.
- Existing claude behavior is byte-identical: claude inject/attach/supervise/quick-exit tests pass UNMODIFIED. If a claude test needs changing, stop — the design is being violated.
- Do NOT restart any leo service. Do NOT touch `~/.leo/leo.yaml` or running agents.
- Commit format `<type>: <description>`, no attribution.
- Non-persistent scheduled tasks (KindTask) are UNCHANGED for all three harnesses (`claude -p` / `codex exec --json` / `opencode run --format json` + ParseEvents).
- Solo-user project: no compat shims, no deprecation paths — delete dead code outright.

## Verified facts (live-tested 2026-07-12 — trust these, do not re-derive)

- codex TUI (0.144.1) in tmux: input marker `› ` (U+203A + space). Probe protocol works: `send-keys -l "."` echoes `› .`; `C-u` clears; `paste-buffer -d` + Enter submits. The TUI renders rotating grey placeholder hints on the input line (e.g. `› Use /skills to list available skills`) — a marker-line-has-content check is NOT sufficient; the probe check must match the probe char exactly.
- codex TUI accepts `--sandbox <mode> -a never -m <model> -c 'mcp_servers.leo.…'` at launch (all honored; MCP config took effect).
- codex trust dialog is skipped iff `~/.codex/config.toml` contains a `[projects."<dir>"]` + `trust_level = "trusted"` entry BEFORE launch. Inline `-c` overrides do NOT skip it. The dialog text contains "trust" so the PaneKey deny-pattern correctly never auto-answers it.
- `codex resume <uuid> [flags…]` (subcommand-first, flags after) restores the conversation.
- codex creates NO rollout file at TUI launch — only when the first turn runs. Rollout path: `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`; line 1 is `{"timestamp":…,"type":"session_meta","payload":{"id":"<uuid>","cwd":"<abs dir>",…}}`.
- opencode TUI (1.17.7) in tmux: input box lines start with `┃`; probe echoes `┃  .`; placeholder is `┃  Ask anything…`. Same paste protocol works. Flags: `--model provider/model`, `-s <session-id>` (resume). No session exists at TUI launch — created on first turn. `opencode session list --format json` (run with cwd=workspace) returns `[{"id":"ses_…","created":<epoch-ms>,"directory":"<abs dir>",…}]`.
- claude machinery is generic except `claudePromptGlyph = "❯ "` in internal/tmux/inject.go.

## File structure

| File | Change |
|---|---|
| internal/tmux/inject.go | Generalize: exported `InputState`, `Profile`, `InjectPromptTUI`, `ProbeClassifier`; `InjectPrompt` becomes the claude-profile wrapper |
| internal/harness/driver.go | Add `PreLauncher`, `SessionArgsRefresher`; (Task 9) delete `DriveStyle`, `Style()`, `SessionHandle.TurnArgs`, collapse `AttachSpec` |
| internal/harness/tmuxtui/tmuxtui.go (new) | Shared TUI driver |
| internal/harness/claude/driver.go | Slims to profile funcs; `Driver()` returns tmuxtui instance |
| internal/harness/codex/{codex,driver,options}.go | TUI argv for session kinds; trust pre-launch; rollout discovery; resume refresher; delete TurnDriver+transcripts |
| internal/harness/opencode/{opencode,driver,options}.go | TUI argv; discovery via session list; `-s` refresher; delete ServerDriver+serverstate.go |
| internal/agent/args.go | Drop opencode EnsureServerState block |
| internal/agent/manager.go | Drop driveTurnsHistoryPath + Logs branch + handle TurnArgs |
| internal/service/process.go | Delete DriveTurns branch + superviseTurnBased; add PreLaunch + RefreshSessionArgs per iteration |
| internal/service/session.go | codex/opencode sessions → generic TUI supervise loop; dispatch simplification |
| internal/service/superviseloop.go | LoopSpec gains optional PreLaunch |
| internal/service/sweep.go | Delete isSweepEligibleHarness (idle-suspend for all) |
| internal/cli/tmux.go, attach.go, agent.go | Attach collapse; delete window-ensure machinery |
| internal/daemon/{types,handlers_agents,client_agents}.go | AttachSpecResponse collapse |
| docs/configuration/harnesses.md | Rewrite session-driver semantics |

---

### Task 1: Generalize tmux injection with per-harness probe profiles

**Files:**
- Modify: `internal/tmux/inject.go`
- Test: `internal/tmux/inject_test.go` (extend existing)

**Interfaces:**
- Produces: `tmux.InputState` (`InputUnknown`, `InputEmpty`, `InputHasContent`), `tmux.Profile{Marker string; Classify func(string) InputState}`, `tmux.InjectPromptTUI(ctx, tmuxPath, session, body string, p Profile) error`, `tmux.ProbeClassifier(marker string) func(string) InputState`, `tmux.ClaudeProfile() Profile`. `tmux.InjectPrompt` keeps its exact current signature/behavior (wrapper over InjectPromptTUI with ClaudeProfile).

- [ ] **Step 1: Write failing tests** — in `internal/tmux/inject_test.go` add (fixtures are REAL pane captures from the 2026-07-12 lab; keep them verbatim):

```go
func TestProbeClassifier(t *testing.T) {
	codexReady := "  Tip: Our most capable model yet.\n› Use /skills to list available skills\n  gpt-5.6-sol default"
	codexProbe := "  Tip: Our most capable model yet.\n› .\n  gpt-5.6-sol default"
	opencodeReady := "┃\n┃  Ask anything... \"Fix a TODO in the codebase\"\n┃\n┃  Build · Qwen 3.6 35B A3B (local)"
	opencodeProbe := "┃\n┃  .\n┃\n┃  Build · Qwen 3.6 35B A3B (local)"
	tests := []struct {
		name, marker, pane string
		want               InputState
	}{
		{"codex placeholder is not probe", "› ", codexReady, InputEmpty},
		{"codex probe landed", "› ", codexProbe, InputHasContent},
		{"opencode placeholder is not probe", "┃", opencodeReady, InputEmpty},
		{"opencode probe landed", "┃", opencodeProbe, InputHasContent},
		{"no marker at all", "› ", "plain output\nno input box", InputUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProbeClassifier(tt.marker)(tt.pane); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
```

Also add a test that `InjectPromptTUI` with a custom profile classifier drives phase 1 with that classifier (mirror the existing `injectPrompt` readiness tests' seam usage — they stub `execCommand`; reuse their helper scaffolding, passing a `Profile` whose Classify returns a canned sequence).

- [ ] **Step 2: Run to verify failure** — `go test -race -run 'TestProbeClassifier|TestInjectPromptTUI' ./internal/tmux/` → FAIL (undefined: ProbeClassifier, InputState, …).

- [ ] **Step 3: Implement** — in `internal/tmux/inject.go`:
  1. Export the state type: replace `type inputState int` + constants with:

```go
// InputState classifies a captured pane's input box during the readiness
// probe.
type InputState int

const (
	InputUnknown    InputState = iota // pane couldn't be read/parsed — don't block on it
	InputEmpty                        // input box present but probe not landed
	InputHasContent                   // input box carries the probe/typed text
)
```

  Update every internal reference (`inputUnknown`→`InputUnknown` etc.).
  2. Add the profile types + generic classifier:

```go
// Profile describes one harness TUI's input-line shape for the readiness
// probe. Marker prefixes the input line. Classify inspects a captured pane
// and reports the input state; harnesses whose input line renders
// placeholder hints (codex, opencode) must use ProbeClassifier, which only
// accepts the exact probe char as "content" — a bare non-empty check would
// mistake the placeholder for a landed probe.
type Profile struct {
	Marker   string
	Classify func(pane string) InputState
}

// ClaudeProfile is claude's probe profile: the existing classifier with its
// menu-option and dialog-chrome guards, unchanged.
func ClaudeProfile() Profile {
	return Profile{Marker: claudePromptGlyph, Classify: classifyInput}
}

// ProbeClassifier returns a classifier for TUIs whose input line starts with
// marker and may render placeholder hints: only an input line whose content
// is exactly the probe char counts as landed; any other content (including
// placeholders, or text a human left in an attached box) reports InputEmpty
// so the probe keeps waiting.
func ProbeClassifier(marker string) func(string) InputState {
	return func(pane string) InputState {
		lines := strings.Split(pane, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimLeft(lines[i], " \t")
			if !strings.HasPrefix(line, marker) {
				continue
			}
			content := strings.TrimSpace(line[len(marker):])
			if content == inputProbe {
				return InputHasContent
			}
			return InputEmpty
		}
		return InputUnknown
	}
}
```

  3. Thread the profile through: `InjectPromptTUI(ctx, tmuxPath, session, body string, p Profile) error` calls the inner `injectPrompt(ctx, tmuxPath, session, body, p, injectReadyAttempts, injectReadyPoll)`; the inner form and `paneInputState` gain the `Profile` parameter and use `p.Classify` instead of the package-level `classifyInput`. `InjectPrompt` (existing exported claude entry point — other call sites depend on it) becomes:

```go
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error {
	return InjectPromptTUI(ctx, tmuxPath, session, body, ClaudeProfile())
}
```

  Error message in phase 1's fail-fast: replace the word "claude" with "session %q's TUI" (it is harness-neutral now).
- [ ] **Step 4: Run tests** — `go test -race ./internal/tmux/` → PASS, including all pre-existing tests unmodified (they exercise the claude profile path).
- [ ] **Step 5: Full gates, then commit** — `feat(tmux): profile-parameterized TUI injection with per-harness probe classifiers`

---

### Task 2: Harness capability interfaces (additive)

**Files:**
- Modify: `internal/harness/driver.go`
- Test: none (interface declarations only; consumers test them)

**Interfaces:**
- Produces: `harness.PreLauncher`, `harness.SessionArgsRefresher`. Everything existing stays untouched in this task (AttachSpec/DriveStyle collapse happens in Task 9 after all consumers migrate).

- [ ] **Step 1: Add to `internal/harness/driver.go`** (after QuickExitRecovery):

```go
// PreLauncher is an optional SessionDriver capability: PreLaunch runs before
// every tmux new-session spawn of this session (fresh and restart alike), in
// the supervisor's goroutine. It must be idempotent and fast — e.g. codex
// registers the workspace as trusted in ~/.codex/config.toml so the TUI
// never blocks on its trust dialog. Errors are logged, never fatal: a failed
// hook degrades to the TUI showing its dialog, which the operator can answer.
type PreLauncher interface {
	PreLaunch(h SessionHandle) error
}

// SessionArgsRefresher is an optional SessionDriver capability for harnesses
// that cannot pin a session id at launch (codex, opencode): the supervisor
// calls it before every spawn to rewrite the launch argv from the currently
// stored session id — adding resume tokens once a post-hoc-discovered id
// exists, and stripping stale ones when the store was cleared. storedID ==
// "" must return argv with no session tokens.
type SessionArgsRefresher interface {
	RefreshSessionArgs(args []string, storedID string) []string
}
```

- [ ] **Step 2: Verify + commit** — `go build ./...` then full gates. Commit: `feat(harness): PreLauncher and SessionArgsRefresher driver capabilities`

---

### Task 3: Shared tmuxtui driver package

**Files:**
- Create: `internal/harness/tmuxtui/tmuxtui.go`
- Test: `internal/harness/tmuxtui/tmuxtui_test.go`

**Interfaces:**
- Consumes: `tmux.Profile`, `tmux.InjectPromptTUI`, `tmux.Locate`, `tmux.AbortPrompt`, `harness.SessionDriver` + optional capabilities.
- Produces: `tmuxtui.Config{Probe tmux.Profile; PaneKeyFn func(string) string; RecoverFn func([]string) ([]string, harness.QuickExitAction); PreLaunchFn func(harness.SessionHandle) error; RefreshArgsFn func([]string, string) []string; DiscoverIDFn func(context.Context, harness.SessionHandle, time.Time) (string, error)}`, `tmuxtui.New(Config) Driver`. `Driver` implements `harness.SessionDriver` (Style() returns harness.DriveTmux until Task 9 removes it), `harness.PaneCare`, `harness.QuickExitRecovery`, `harness.TurnAborter`, `harness.PreLauncher`, `harness.SessionArgsRefresher`. Test seams: `SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string, p tmux.Profile) error) func()`, `SetLocateTmuxForTest(fn func() (string, error)) func()`, and vars `discoverPoll`/`discoverBudget`.

- [ ] **Step 1: Write failing tests** — `internal/harness/tmuxtui/tmuxtui_test.go`. Use a fake `harness.SessionIDStore` (map-backed). Cover:

```go
// - Inject delegates to the inject seam with the configured profile and
//   returns (nil, nil) on success (fire-and-forget).
// - Start with OpeningPrompt set and empty IDs injects the prompt once;
//   with a stored id it injects nothing (restart-safe).
// - Start with DiscoverIDFn and empty IDs polls until the fn returns an id,
//   then IDs.Set(id) (shrink discoverPoll/discoverBudget in the test).
// - Start with a stored id never calls DiscoverIDFn.
// - Inject with DiscoverIDFn and empty IDs triggers one discovery loop
//   (poll until set), and a second concurrent Inject does not start a second
//   loop (in-flight dedupe).
// - Attach returns AttachSpec{TmuxSession: h.TmuxSession} (other fields zero).
// - PaneKey/RecoverQuickExit/PreLaunch/RefreshSessionArgs delegate to the
//   configured fns; nil fns → "" / (args, QuickExitClearSession) / nil / args.
// - AbortTurn calls tmux.AbortPrompt via the locate seam (stub execCommand
//   is not needed: stub locate to error and assert the error surfaces).
```

Write them as real Go tests (table-driven where natural).

- [ ] **Step 2: RED** — `go test -race ./internal/harness/tmuxtui/` → FAIL (package missing).
- [ ] **Step 3: Implement** `internal/harness/tmuxtui/tmuxtui.go`:

```go
// Package tmuxtui is the shared session driver for every harness that runs
// its interactive TUI as the supervised process inside a leo tmux session
// (claude, codex, opencode). Per-harness differences ride in through Config:
// the readiness-probe profile, dialog policy, quick-exit recovery, workspace
// pre-launch hook, session-args refresh, and post-hoc session-id discovery.
package tmuxtui

import (
	"context"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// Seams tests replace; production uses the real tmux helpers.
var (
	injectPromptFn = tmux.InjectPromptTUI
	locateTmuxFn   = tmux.Locate
)

// discoverPoll/discoverBudget bound the post-launch session-id discovery
// loop. Vars so tests shrink them. The budget is generous because a session
// id only exists after the FIRST turn runs — a process with no opening
// prompt may idle unbounded before its first injected message; Inject
// re-arms discovery in that case, so a Start-loop miss is not fatal.
var (
	discoverPoll   = 2 * time.Second
	discoverBudget = 5 * time.Minute
)

// discovering dedupes in-flight discovery loops per tmux session.
var discovering sync.Map // TmuxSession → struct{}

type Config struct {
	Probe         tmux.Profile
	PaneKeyFn     func(pane string) string
	RecoverFn     func(args []string) ([]string, harness.QuickExitAction)
	PreLaunchFn   func(h harness.SessionHandle) error
	RefreshArgsFn func(args []string, storedID string) []string
	DiscoverIDFn  func(ctx context.Context, h harness.SessionHandle, since time.Time) (string, error)
}

type Driver struct{ cfg Config }

func New(cfg Config) Driver { return Driver{cfg: cfg} }

func (d Driver) Style() harness.DriveStyle { return harness.DriveTmux }

// Start delivers the opening prompt (once, guarded by the empty IDs store so
// restarts never replay it) and arms session-id discovery for harnesses that
// can't pin an id at launch.
func (d Driver) Start(ctx context.Context, h harness.SessionHandle) error {
	since := time.Now().Add(-2 * time.Minute) // slack: spawn preceded Start
	if h.OpeningPrompt != "" && h.IDs.Get() == "" {
		if _, err := d.Inject(ctx, h, h.OpeningPrompt); err != nil {
			return err
		}
	}
	d.maybeDiscover(ctx, h, since)
	return nil
}

// Inject pastes msg into the live TUI (readiness-probed). Delivery is
// asynchronous — the turn outcome lives in the pane — so Result is always
// nil (claude parity for every harness).
func (d Driver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return nil, err
	}
	if err := injectPromptFn(ctx, tmuxPath, h.TmuxSession, msg, d.cfg.Probe); err != nil {
		return nil, err
	}
	d.maybeDiscover(ctx, h, time.Now().Add(-2*time.Minute))
	return nil, nil
}

// maybeDiscover starts one background discovery loop when the harness needs
// post-hoc id discovery and no id is stored yet. Deduped per tmux session.
func (d Driver) maybeDiscover(ctx context.Context, h harness.SessionHandle, since time.Time) {
	if d.cfg.DiscoverIDFn == nil || h.IDs.Get() != "" {
		return
	}
	if _, loaded := discovering.LoadOrStore(h.TmuxSession, struct{}{}); loaded {
		return
	}
	go func() {
		defer discovering.Delete(h.TmuxSession)
		deadline := time.Now().Add(discoverBudget)
		for {
			if id, err := d.cfg.DiscoverIDFn(ctx, h, since); err == nil && id != "" {
				h.IDs.Set(id)
				return
			}
			if time.Now().After(deadline) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(discoverPoll):
			}
		}
	}()
}

func (d Driver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{TmuxSession: h.TmuxSession}, nil
}

// AbortTurn cancels a mid-turn TUI by sending Escape then Ctrl-C — the same
// keys for every harness.
func (d Driver) AbortTurn(h harness.SessionHandle) error {
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return err
	}
	return tmux.AbortPrompt(context.Background(), tmuxPath, h.TmuxSession)
}

func (d Driver) PaneKey(pane string) string {
	if d.cfg.PaneKeyFn == nil {
		return ""
	}
	return d.cfg.PaneKeyFn(pane)
}

func (d Driver) RecoverQuickExit(args []string) ([]string, harness.QuickExitAction) {
	if d.cfg.RecoverFn == nil {
		return args, harness.QuickExitClearSession
	}
	return d.cfg.RecoverFn(args)
}

func (d Driver) PreLaunch(h harness.SessionHandle) error {
	if d.cfg.PreLaunchFn == nil {
		return nil
	}
	return d.cfg.PreLaunchFn(h)
}

func (d Driver) RefreshSessionArgs(args []string, storedID string) []string {
	if d.cfg.RefreshArgsFn == nil {
		return args
	}
	return d.cfg.RefreshArgsFn(args, storedID)
}

// SetInjectPromptForTest / SetLocateTmuxForTest swap the seams and return a
// restore func (same convention as the claude driver's former seams).
func SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string, p tmux.Profile) error) func() {
	prev := injectPromptFn
	injectPromptFn = fn
	return func() { injectPromptFn = prev }
}

func SetLocateTmuxForTest(fn func() (string, error)) func() {
	prev := locateTmuxFn
	locateTmuxFn = fn
	return func() { locateTmuxFn = prev }
}

```

(`log` is not imported — the discovery loop is silent by design; a missed discovery just re-arms on the next Inject.)

- [ ] **Step 4: GREEN** — `go test -race ./internal/harness/tmuxtui/` → PASS.
- [ ] **Step 5: Full gates, commit** — `feat(harness): shared tmuxtui session driver with per-harness profiles`

---

### Task 4: Rewire claude onto the shared driver (behavior-neutral)

**Files:**
- Modify: `internal/harness/claude/driver.go`, `internal/harness/claude/claude.go` (the `Driver()` method), claude driver tests
- Test: existing `internal/harness/claude/driver_test.go`

**Interfaces:**
- Consumes: `tmuxtui.New`, `tmux.ClaudeProfile`.
- Produces: `claude.DialogKey(pane string) string` and `claude.RecoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction)` (package funcs — the former methods).

- [ ] **Step 1: Convert methods to package funcs** — in `internal/harness/claude/driver.go`: rename `(TmuxTUIDriver) PaneKey` → `func DialogKey(pane string) string` and `(TmuxTUIDriver) RecoverQuickExit` → `func RecoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction)` (bodies unchanged, doc comments kept). Delete the `TmuxTUIDriver` struct, its `Style/Start/Inject/Attach` methods, and the `injectPromptFn`/`locateTmuxFn` seams + `SetInjectPromptForTest`/`SetLocateTmuxForTest` (the shared driver owns those now). Update `Driver()` in claude.go:

```go
// Driver: the shared tmux-TUI driver with claude's probe profile, dialog
// policy, and --session-id → --resume → fresh quick-exit ladder.
func (Claude) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:     tmux.ClaudeProfile(),
		PaneKeyFn: DialogKey,
		RecoverFn: RecoverQuickExitArgs,
	})
}
```

- [ ] **Step 2: Migrate claude driver tests** — PaneKey/RecoverQuickExit tests call the new package funcs (assert identical cases); Inject/Attach tests move to asserting via the driver returned by `Claude{}.Driver()` with the tmuxtui seams (or are deleted where tmuxtui_test.go already covers the identical behavior — prefer deleting duplicates). The supervisor/agent/daemon tests that stub claude's old seams must switch to `tmuxtui.SetInjectPromptForTest` — grep: `grep -rn "SetInjectPromptForTest\|SetLocateTmuxForTest" --include="*.go" | grep -v tmuxtui`. Update every hit.
- [ ] **Step 3: GREEN + gates** — `go test -race ./...` all packages. The claude behavior tests (ladder cases, dialog cases) pass with identical expectations.
- [ ] **Step 4: Commit** — `refactor(claude): drive sessions through the shared tmuxtui driver`

---

### Task 5: Codex adapter — TUI argv, trust pre-launch, rollout discovery, resume refresher

**Files:**
- Modify: `internal/harness/codex/codex.go`, `internal/harness/codex/options.go`
- Rewrite: `internal/harness/codex/driver.go` (delete TurnDriver, transcripts, turn machinery)
- Test: `internal/harness/codex/driver_test.go` (rewrite), `codex_test.go` (extend), plus update `internal/service/session_test.go` codex-dispatch argv expectations if any assert the old `exec --json` session argv.

**Interfaces:**
- Consumes: `tmuxtui.New`, `tmux.ProbeClassifier`, existing `Options`/`LeoMCPBridge.configArgs()`.
- Produces: `Codex{}.Args(spec)` for Kind{Process,Agent,Session} returns `["-a","never", ("--model",m)?, ("--sandbox",s)?, <mcp -c args>…]` (no `exec`, no `--json`, no prompt, no resume tokens). `SessionArgs` unchanged (`["resume", id]`). Package funcs: `refreshSessionArgs(args []string, storedID string) []string`, `ensureWorkspaceTrusted(h harness.SessionHandle) error` (seam `var codexConfigPath func() (string, error)`), `discoverSessionID(ctx context.Context, h harness.SessionHandle, since time.Time) (string, error)` (seam `var codexSessionsDir func() (string, error)`).

- [ ] **Step 1: Write failing tests**

```go
func TestArgsSessionKindsBuildTUIArgv(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindAgent, Model: "gpt-5.6-sol",
		Options: Options{Sandbox: "workspace-write"}}
	args, err := Codex{}.Args(spec)
	if err != nil { t.Fatal(err) }
	want := []string{"-a", "never", "--model", "gpt-5.6-sol", "--sandbox", "workspace-write"}
	if !reflect.DeepEqual(args, want) { t.Errorf("got %v want %v", args, want) }
}

func TestRefreshSessionArgs(t *testing.T) {
	base := []string{"-a", "never", "--model", "m"}
	tests := []struct{ name string; in []string; id string; want []string }{
		{"fresh no id", base, "", base},
		{"fresh gains resume", base, "u1", []string{"resume", "u1", "-a", "never", "--model", "m"}},
		{"stale resume replaced", []string{"resume", "old", "-a", "never"}, "u2", []string{"resume", "u2", "-a", "never"}},
		{"resume stripped when id cleared", []string{"resume", "old", "-a", "never"}, "", []string{"-a", "never"}},
	}
	// … table body asserting refreshSessionArgs
}

func TestEnsureWorkspaceTrusted(t *testing.T) {
	// temp config.toml via the codexConfigPath seam:
	// (a) missing file → created with the [projects."<ws>"] block, perms 0600
	// (b) entry already present → file byte-identical after call (idempotent)
	// (c) existing unrelated content → preserved, block appended
}

func TestDiscoverSessionID(t *testing.T) {
	// temp sessions dir via codexSessionsDir seam, laid out YYYY/MM/DD/:
	// (a) one rollout with matching cwd, mtime after since → its uuid returned
	// (b) rollout with matching cwd but mtime BEFORE since → ""
	// (c) rollout with non-matching cwd → ""
	// (d) two matching rollouts → newest-by-mtime wins (and a warning is logged)
	// First line of each fixture file:
	// {"timestamp":"…","type":"session_meta","payload":{"id":"<uuid>","cwd":"<dir>"}}
}
```

Also: quick-exit ladder test (`resume` args → stripped + `QuickExitClearAndNoResume`; no resume → `QuickExitClearSession`), and a `Driver()` smoke test asserting it implements `harness.PreLauncher`, `harness.SessionArgsRefresher`, `harness.PaneCare`.

- [ ] **Step 2: RED** — `go test -race ./internal/harness/codex/` → FAIL.
- [ ] **Step 3: Implement.**
  - `codex.go` `Args()`: keep the KindTask branch byte-identical (`exec --json --skip-git-repo-check …` + SessionArgs + prompt). Replace the session-kinds branch:

```go
	if spec.Kind == harness.KindProcess || spec.Kind == harness.KindAgent || spec.Kind == harness.KindSession {
		// Interactive TUI argv. -a never keeps an unattended TUI from ever
		// blocking on an approval prompt (parity with headless exec, which
		// always ran approval policy "never"). Resume tokens are added by
		// the supervisor via RefreshSessionArgs once a session id is
		// discovered; the opening prompt is injected by the driver's Start.
		args := []string{"-a", "never"}
		if spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		if opts.Sandbox != "" {
			args = append(args, "--sandbox", opts.Sandbox)
		}
		return append(args, opts.LeoMCP.configArgs()...), nil
	}
```

  Update the package doc comment (turn-per-invocation → tmux TUI) and `Driver()`:

```go
func (Codex) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:         tmux.Profile{Marker: "› ", Classify: tmux.ProbeClassifier("› ")},
		RecoverFn:     recoverQuickExitArgs,
		PreLaunchFn:   ensureWorkspaceTrusted,
		RefreshArgsFn: refreshSessionArgs,
		DiscoverIDFn:  discoverSessionID,
	})
}
```

  - New `driver.go` (replacing the whole file):

```go
// Package-file: codex's tmuxtui profile hooks. The TUI cannot pin a session
// id at launch, so the id is discovered post-hoc from rollout files and
// resume rides in as the `resume <id>` subcommand prefix on relaunch.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// codexConfigPath/codexSessionsDir are seams tests replace with temp dirs.
var codexConfigPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

var codexSessionsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// ensureWorkspaceTrusted idempotently registers h.Workspace as trusted in
// ~/.codex/config.toml so the TUI skips its trust dialog (which the dialog
// policy correctly refuses to auto-answer — it contains "trust"). This is
// the same write codex itself performs when the user answers "Yes"; inline
// -c overrides do NOT skip the dialog (verified 2026-07-12).
func ensureWorkspaceTrusted(h harness.SessionHandle) error {
	path, err := codexConfigPath()
	if err != nil {
		return fmt.Errorf("codex: resolving config path: %w", err)
	}
	header := fmt.Sprintf("[projects.%q]", h.Workspace)
	existing, err := os.ReadFile(path) // #nosec G304 -- fixed well-known path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: reading %s: %w", path, err)
	}
	if strings.Contains(string(existing), header) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("codex: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return fmt.Errorf("codex: opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n%s\ntrust_level = \"trusted\"\n", header); err != nil {
		return fmt.Errorf("codex: writing trust entry: %w", err)
	}
	return nil
}

// refreshSessionArgs rewrites the launch argv from the stored session id:
// prefix `resume <id>` (codex resumes via a subcommand, flags stay valid
// after it — verified 2026-07-12), replacing any stale resume prefix.
func refreshSessionArgs(args []string, storedID string) []string {
	base := args
	if len(base) >= 2 && base[0] == "resume" {
		base = base[2:]
	}
	if storedID == "" {
		return append([]string{}, base...)
	}
	return append([]string{"resume", storedID}, base...)
}

// recoverQuickExitArgs: a quick exit while resuming means the resume itself
// is poisoned — strip it, clear the stored id, and mark no-resume (mirrors
// claude's ladder step 2). A fresh launch that quick-exits just clears.
func recoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction) {
	if len(args) >= 2 && args[0] == "resume" {
		return append([]string{}, args[2:]...), harness.QuickExitClearAndNoResume
	}
	return args, harness.QuickExitClearSession
}

// rolloutMeta is the shape of a rollout file's first line.
type rolloutMeta struct {
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

// discoverSessionID finds the newest rollout created at/after `since` whose
// recorded cwd is h.Workspace. Rollouts only exist once the FIRST turn runs
// (verified: TUI launch alone creates none), so callers poll. Two agents
// sharing a workspace can race here — newest wins and a warning is logged;
// the residual ambiguity is accepted (see spec Risks).
func discoverSessionID(_ context.Context, h harness.SessionHandle, since time.Time) (string, error) {
	root, err := codexSessionsDir()
	if err != nil {
		return "", err
	}
	var bestID string
	var bestMod time.Time
	matches := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		id, cwd, ok := readRolloutMeta(path)
		if !ok || !samePath(cwd, h.Workspace) {
			return nil
		}
		matches++
		if info.ModTime().After(bestMod) {
			bestMod = info.ModTime()
			bestID = id
		}
		return nil
	})
	if matches > 1 {
		log.Printf("codex: %d rollouts match workspace %s since %s; using newest (%s)", matches, h.Workspace, since.Format(time.RFC3339), bestID)
	}
	return bestID, nil
}

// readRolloutMeta parses a rollout file's first line for the session id and
// cwd. ok=false on any parse trouble — discovery just keeps polling.
func readRolloutMeta(path string) (id, cwd string, ok bool) {
	f, err := os.Open(path) // #nosec G304 -- path enumerated from codex's own sessions dir
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return "", "", false
	}
	var meta rolloutMeta
	if json.Unmarshal(sc.Bytes(), &meta) != nil {
		return "", "", false
	}
	return meta.Payload.ID, meta.Payload.Cwd, meta.Payload.ID != "" && meta.Payload.Cwd != ""
}

// samePath reports whether a and b refer to the same filesystem location.
// A plain Clean comparison is insufficient: codex records its own
// os.Getwd()-derived path, and on macOS /tmp symlinks to /private/tmp.
// Falls back to the Clean comparison when EvalSymlinks fails on either side
// (e.g. the dir no longer exists) — a missed match here is tolerable, the
// caller just keeps polling. (Same shape as opencode's samePath, which
// stays in its own package.)
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, aErr := filepath.EvalSymlinks(a)
	rb, bErr := filepath.EvalSymlinks(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return ra == rb
}
```

  Delete from the old driver.go: `TurnDriver`, `execCommand`, `turnWaitDelay`, `turnMu`, `aborts`, `lockFor`, `runTurn`, `transcriptPath`, `appendTranscript`, `envSlice`, the old `Attach`/`AbortTurn`. Delete `testdata` fixtures that only served TurnDriver tests. Note `ParseEvents` (parse.go) stays — the KindTask path uses it.
  - `options.go`: update the `"approval"` rejection message to `option %q is not supported: leo always launches codex with approval policy "never" (unattended sessions)`. `LeoMCPBridge`/`configArgs` unchanged (verified to work at TUI launch).
- [ ] **Step 4: GREEN** — `go test -race ./internal/harness/codex/ ./internal/service/` (fix codex-dispatch argv expectations in service tests to the new TUI shape).
- [ ] **Step 5: Full gates, commit** — `feat(codex): drive sessions as the codex TUI in tmux`

---

### Task 6: Opencode adapter — TUI argv, discovery, refresher; delete server machinery

**Files:**
- Modify: `internal/harness/opencode/opencode.go`, `options.go`
- Rewrite: `internal/harness/opencode/driver.go`
- Delete: `internal/harness/opencode/serverstate.go`, `serverstate_test.go`
- Modify: `internal/agent/args.go` (drop EnsureServerState block), `internal/service/session.go` (drop `buildOpencodeSessionLaunch`/`superviseOpencodeSession` — replaced in Task 7 by the generic path; in THIS task make `SuperviseSession`'s opencode branch temporarily return nil like codex so the package compiles)
- Test: `internal/harness/opencode/driver_test.go` (rewrite), `opencode_test.go`, `internal/agent/args_test.go`

**Interfaces:**
- Produces: `Opencode{}.Args(spec)` for session kinds returns `[("--model", m)?]`. `SessionArgs` unchanged (`["-s", id]`). Package funcs `refreshSessionArgs(args, storedID) []string` (strip/append the `-s <id>` pair), `discoverSessionID(ctx, h, since) (string, error)` (wraps the existing `latestSessionIDForDir` with a `created >= since` filter — created is epoch-MILLISECONDS). `Options` loses `ServerPort`/`ServerPassword`; `Env()` loses the password branch (keeps `OPENCODE_CONFIG_CONTENT` with MCP + permission).

- [ ] **Step 1: Failing tests** — mirror Task 5's shape: session-kind argv (`{"--model", "lmstudio/qwen/qwen3.6-35b-a3b"}` / empty when no model); refreshSessionArgs table (`-s` pair added/replaced/stripped — note the pair may sit anywhere in argv, strip by scanning like claude's `stripResumeArg`); discovery honoring `since` (fixture via the existing `execCommand` seam feeding canned `session list` JSON with `created` values straddling `since.UnixMilli()`); quick-exit ladder (`-s` present → stripped + `QuickExitClearAndNoResume`; absent → `QuickExitClearSession`); `Env()` no longer emits `OPENCODE_SERVER_PASSWORD`; `Driver()` capability smoke test.
- [ ] **Step 2: RED**, then implement:
  - `opencode.go`: session-kinds branch of `Args()` becomes:

```go
	if spec.Kind == harness.KindProcess || spec.Kind == harness.KindAgent || spec.Kind == harness.KindSession {
		// Interactive TUI argv; workspace rides in as tmux new-session's -c
		// cwd. Resume (-s) is added by RefreshSessionArgs once a session id
		// is discovered; the opening prompt is injected by the driver's
		// Start. Permissions and the leo MCP bridge ride in via the
		// OPENCODE_CONFIG_CONTENT env overlay (Env), unchanged.
		var args []string
		if spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		return args, nil
	}
```

  `Driver()`:

```go
func (Opencode) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:         tmux.Profile{Marker: "┃", Classify: tmux.ProbeClassifier("┃")},
		RecoverFn:     recoverQuickExitArgs,
		RefreshArgsFn: refreshSessionArgs,
		DiscoverIDFn:  discoverSessionID,
	})
}
```

  - `driver.go`: keep `execCommand` seam, `latestSessionIDForDir` (add a `sinceMillis int64` parameter filtering `e.Created >= sinceMillis`), and `samePath`. Add `refreshSessionArgs`, `recoverQuickExitArgs`, `discoverSessionID` (thin wrapper: `latestSessionIDForDir(ctx, h.Workspace, since.UnixMilli())`, returning `("", nil)` on no match). Delete: `ServerDriver`, `lookPath`, `turnMu`, `aborts`, `lockFor`, health-poll machinery, `Inject`, `Attach`, `Start`, `AbortTurn`, `waitForHealth`, `isHealthy`, `serverBasicAuthUser`, `turnWaitDelay`.
  - `options.go`: delete `ServerPort`/`ServerPassword` fields and `Env()`'s password branch; update doc comments.
  - `internal/agent/args.go`: in the `opencodeharness.Options` case delete the `EnsureServerState` block (keep the LeoMCP bridge wiring); fix its doc comment.
  - `internal/service/session.go`: change `SuperviseSession`'s `case "opencode":` to `return nil` (temporary — Task 7 replaces both non-claude branches with the generic loop) and delete `buildOpencodeSessionLaunch` + `superviseOpencodeSession` + their tests.
- [ ] **Step 3: GREEN + full gates** (agent/service tests updated for the removed serverstate).
- [ ] **Step 4: Commit** — `feat(opencode): drive sessions as the opencode TUI in tmux; delete server machinery`

---

### Task 7: Supervisor unification — processes and persistent sessions

**Files:**
- Modify: `internal/service/process.go`, `internal/service/session.go`, `internal/service/superviseloop.go`
- Test: `internal/service/process_test.go`, `session_test.go` (extend; the packages have rich stub scaffolding — `supervisedExecFn`, tmux stub scripts from `harness_binary_test.go`, `loopExecCommand`)

**Interfaces:**
- Consumes: `harness.PreLauncher`, `harness.SessionArgsRefresher`, adapters' new session-kind `Args()`.
- Produces: `LoopSpec.PreLaunch func() error` (optional, runs before every tmux new-session in `runSuperviseLoop`); `superviseTUISession(ctx, tmuxPath, spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) error` handling codex AND opencode sessions.

- [ ] **Step 1: Failing tests**
  - process: a fake driver implementing PreLauncher + SessionArgsRefresher (register a fake harness or stub `driverFor`) — assert (a) PreLaunch runs before the tmux new-session invocation, (b) with a stored id in the session store, the spawned shell command contains the refreshed tokens, (c) with no stored id, argv is unchanged, (d) the old DriveTurns branch is gone (a codex-harness spec now spawns a tmux session running the codex binary — extend `harness_binary_test.go`'s stub-tmux capture to assert `codex -a never …` in the new-session command, replacing any test that asserted codex specs skip tmux).
  - session: `SuperviseSession` with a codex/opencode spec starts the generic loop: stub `loopExecCommand`, assert new-session command contains the harness binary + TUI argv + env exports (`LEO_SESSION_NAME`, harness Env overlay, spec env), and that a stored session id in the store surfaces as resume tokens on the NEXT loop iteration's shell cmd.
- [ ] **Step 2: RED**, implement:
  - `process.go`: delete lines the DriveTurns branch (`if drv != nil && drv.Style() == harness.DriveTurns { … }`) and `superviseTurnBased`. In the spawn else-branch, immediately before `buildClaudeShellCmd`:

```go
			if pl, ok := drv.(harness.PreLauncher); ok {
				if err := pl.PreLaunch(handleForSpec(spec, id, homePath)); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] pre-launch: %v\n", name, err)
				}
			}
			if rf, ok := drv.(harness.SessionArgsRefresher); ok {
				currentArgs = rf.RefreshSessionArgs(currentArgs, agentOrProcessIDs(homePath, name).Get())
				id.setArgs(currentArgs)
			}
```

  (claude's driver has no RefreshArgsFn → returns args unchanged → claude behavior identical.)
  - `superviseloop.go`: add `PreLaunch func() error` to LoopSpec (doc: optional; runs before every tmux new-session; errors logged, not fatal) and invoke it in `runSuperviseLoop` right before the new-session exec.
  - `session.go`: `SuperviseSession` collapses to:

```go
	switch spec.Harness {
	case "", "claude":
		return superviseClaudeSession(ctx, tmuxPath, claudePath, spec, homePath, onSessionEnd)
	default:
		return superviseTUISession(ctx, tmuxPath, spec, homePath, cfg, webToken, onSessionEnd)
	}
```

  `superviseTUISession` builds per-harness launch pieces (options decode + LeoMCP bridge wiring — codex: the `LeoMCPBridge{Command:"leo",Args:["mcp-server"],EnvVars:[…],ApprovalMode:"approve"}` + merge `sessionLeoMCPEnv` values into the exported env; opencode: the existing bridge shape from the deleted `buildOpencodeSessionLaunch`, minus ServerPort/Password), computes `baseArgs, hEnv` via `h.Args`/`h.Env` with `Kind: KindSession`, resolves the driver once, and runs:

```go
	store := session.NewStore(homePath)
	drv := h.Driver()
	buildShell := func(resume bool) string {
		args := baseArgs
		if rf, ok := drv.(harness.SessionArgsRefresher); ok {
			id := ""
			if resume {
				id, _ = store.Get("session:" + spec.Name)
			}
			args = rf.RefreshSessionArgs(baseArgs, id)
		}
		shellCmd := shellQuote(binPath)
		for _, a := range args {
			shellCmd += " " + shellQuote(a)
		}
		envExports := fmt.Sprintf("export LEO_SESSION_NAME=%s; export LEO_HOME=%s;",
			shellQuote(spec.Name), shellQuote(homePath))
		for k, v := range hEnv {
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
		for k, v := range leoEnv { // codex MCP env values; empty map for opencode
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
		for k, v := range spec.Env {
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
		return envExports + " exec " + shellCmd
	}
	loop := LoopSpec{
		Name:        spec.Name,
		SessionName: SessionTmuxName(spec.Name),
		Workdir:     spec.Workdir,
		ShellCmd:    buildShell,
		PreLaunch: func() error {
			if pl, ok := drv.(harness.PreLauncher); ok {
				return pl.PreLaunch(harness.SessionHandle{Kind: harness.KindSession, Name: spec.Name,
					TmuxSession: SessionTmuxName(spec.Name), Workspace: spec.Workdir, HomePath: homePath})
			}
			return nil
		},
		OnQuickExit:  func() { _ = session.NewStore(homePath).Delete("session:" + spec.Name) },
		OnSessionEnd: onSessionEnd,
	}
	go runSuperviseLoop(ctx, tmuxPath, loop)
	return nil
```

  `binPath` resolves like the process path: `exec.LookPath(h.Binary())` falling back to the bare name. Also simplify `BuildSessionDispatch`'s codex case to match opencode's (handle only — delete the options-decode/Args/TurnArgs block and its stale doc paragraphs); the dispatch table is now purely "route Inject/Abort to the harness driver with these coordinates".
- [ ] **Step 3: GREEN + full gates.**
- [ ] **Step 4: Commit** — `feat(service): supervise codex/opencode TUIs through the generic loop`

---

### Task 8: Agent package cleanup + inject-result audit

**Files:**
- Modify: `internal/agent/manager.go`, `internal/service/sweep.go`, `internal/service/agents.go` (verify only)
- Test: `internal/agent/manager_test.go`, `internal/service/sweep_test.go`

- [ ] **Step 1: Failing tests** — (a) `Logs` for a codex-harness record uses tmux capture-pane (stub tmuxPath with the capture script pattern from existing tests) — no transcript branch; (b) idle sweep acts on codex/opencode records exactly like claude ones (adapt the existing sweep tests' harness cases).
- [ ] **Step 2: Implement**
  - `manager.go`: delete `driveTurnsHistoryPath` and the Logs branch that calls it (Logs goes straight to capture-pane); delete `TurnArgs: rec.ClaudeArgs` from `handleForRecord` only in Task 9 (field still exists — leave it until then). Resume() needs no change (non-claude records already spawn with stored args; the supervisor's refresher adds resume tokens).
  - `sweep.go`: delete `isSweepEligibleHarness` and its call site + stale comment ("codex is turn-driven…").
  - `internal/service/agents.go` RestoreAgents: VERIFY the existing isClaude guard on `ResumeArgs` (line ~129) leaves non-claude records' args untouched — that is now correct behavior (refresher handles resume); update the comment to say so. No logic change expected.
  - Audit inject call sites for non-nil-Result assumptions: `grep -rn "\.Inject(" --include="*.go" | grep -v _test | grep -v internal/harness`. Every driver Inject now returns nil Result (claude parity). Fix any site that dereferences the Result (expected: none — the claude path always returned nil — but a codex/opencode-specific web/daemon path may echo Result.Text; make it tolerate nil by sending the "delivered" acknowledgement claude sessions produce).
- [ ] **Step 3: GREEN + full gates.**
- [ ] **Step 4: Commit** — `feat(agent): uniform logs and idle-suspend across harnesses`

---

### Task 9: Attach collapse — one tmux attach for every harness

**Files:**
- Modify: `internal/harness/driver.go` (destructive cleanup), `internal/cli/tmux.go`, `internal/cli/attach.go`, `internal/cli/agent.go`, `internal/daemon/types.go`, `internal/daemon/handlers_agents.go`, `internal/daemon/client_agents.go`, `internal/agent/manager.go` + `internal/service/process.go` (drop TurnArgs from handle builders)
- Test: rewrite `internal/cli/attach_driver_test.go` (and neighbors), `internal/daemon/handlers_agents_attach_test.go`

- [ ] **Step 1: Failing tests** — (a) daemon attach-spec endpoint for a codex/opencode agent returns `{name, harness, tmux_session}` and nothing else; (b) CLI `attachViaDriver` with a TmuxSession spec routes to `attachTmuxSession` (assert via the existing exec seams that the tmux attach argv fires and no `new-window`/`list-windows` calls happen); (c) `--cc` continues to work through the same path (it's a plain tmux attach now).
- [ ] **Step 2: Implement**
  - `internal/harness/driver.go`: delete `DriveStyle`, both constants, and `Style()` from `SessionDriver`; delete `SessionHandle.TurnArgs`; collapse:

```go
// AttachSpec says how a caller attaches to a live session: every harness
// runs its TUI inside the leo tmux session, so attach is a tmux attach.
type AttachSpec struct {
	TmuxSession string
}
```

  - Remove `Style()` from tmuxtui.Driver; remove `TurnArgs:` from `handleForSpec` (process.go) and `handleForRecord` (manager.go); fix the `SessionHandle` doc comment.
  - `internal/cli/tmux.go`: `attachViaDriver` shrinks to:

```go
// attachViaDriver attaches to a driver-reported session. Every harness's
// AttachSpec is a tmux session (the TUI lives in the supervised pane), so
// this delegates to attachTmuxSession, which owns every attach flavor
// (nested-tmux popup, --cc control mode, terminfo fallback). Remote clients
// never reach here — `leo agent attach` delegates the whole command to the
// host-side leo (#104).
func attachViaDriver(res config.HostResolution, spec harness.AttachSpec, opts attachOptions) error {
	if spec.TmuxSession == "" {
		return fmt.Errorf("driver returned no attachable tmux session")
	}
	return attachTmuxSession(res, spec.TmuxSession, opts)
}
```

  Delete: `attachViaDriverTmux`, `ensureTmuxWindow`, `tmuxWindowKey`, `windowTarget`, `leoTUIWindowKeyOption`, the ssh/exec argv branches of the old attachViaDriver, and `printAttachHistory` (find it in attach.go/tmux.go). Update `attachLocal` (agent.go) and any process-attach resolver to build `harness.AttachSpec{TmuxSession: resp.TmuxSession}`.
  - `internal/daemon/types.go`: `AgentAttachSpecResponse` keeps `Name`, `Harness`, `TmuxSession` only. `handlers_agents.go` copies just TmuxSession; update its doc comment (claude records still return an empty spec — CLI's claude flow doesn't call this endpoint). `client_agents.go` mirrors.
  - Delete the #106 window-machinery tests; port any still-relevant assertions (e.g. "remote non---cc delegates via ssh") unchanged — those live in agent.go's remote leg, untouched here.
- [ ] **Step 3: GREEN + full gates** (this is the task where `go build ./...` catches every straggler still referencing Style/DriveTurns/TurnArgs/window fields — fix each by deletion, not shims).
- [ ] **Step 4: Commit** — `refactor(harness): collapse AttachSpec to tmux attach; delete DriveStyle and window machinery`

---

### Task 10: Docs + final sweep

**Files:**
- Modify: `docs/configuration/harnesses.md`, `CLAUDE.md` (repo — the "harness" paragraph if it mentions driver styles)
- Verify-only sweep across the repo

- [ ] **Step 1: Rewrite `docs/configuration/harnesses.md`'s "Session driver semantics" section** — all three harnesses: resident interactive TUI supervised in `leo-<name>` tmux; injection via readiness-probed paste; attach = tmux attach everywhere; per-harness notes: claude pins `--session-id` (unchanged ladder); codex discovers its session id from rollout files after the first turn, resumes via `codex resume <id>`, workspaces auto-trusted in `~/.codex/config.toml`, always launches `-a never`; opencode discovers via `opencode session list`, resumes via `-s`, config/permissions/MCP ride `OPENCODE_CONFIG_CONTENT`. Update the support-matrix footnote about idle_suspend (now supported on all harnesses). Remove every mention of `opencode serve`, ports, passwords, transcripts, "no live attach".
- [ ] **Step 2: Sweep** — `grep -rn "DriveTurns\|ServerState\|superviseTurnBased\|HistoryPath\|WindowKey\|isSweepEligible\|transcripts" --include="*.go" --include="*.md" .` → every hit is either deleted code (fix), a stale comment (fix), or the release-notes/state-cleanup note below (fine). Also grep `opencode serve` in docs.
- [ ] **Step 3: State-dir note** — add a line to the plan-completion notes (progress ledger, not code): after updating, stale `~/.leo/state/opencode/*.json` and `~/.leo/state/transcripts/*.log` are inert and can be deleted manually; running codex/opencode agents must be stopped and respawned.
- [ ] **Step 4: Full gates, commit** — `docs: uniform tmux-TUI driver semantics`

---

### Task 11: Integration verification (orchestrator-run, not subagent)

- [ ] Full gates on the branch head; `make e2e`; CI-parity lint (golangci-lint 2.12.2 + pinned gosec).
- [ ] Live verification on Dionysus against the isolated test daemon (NEVER the production service): spawn one agent per harness; per agent verify — inject via leo_send_message → reply appears in pane; `leo agent logs` shows pane content; `leo agent attach` lands in the TUI with tmux status bar (local + from Evan's laptop); kill the pane process → supervisor respawns with resume tokens and conversation intact; suspend/resume for codex + opencode.
- [ ] PR with the full-branch diff; Opus code review; Evan merges.
