# Agent Idle-Suspend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in "suspended" state for ephemeral agents — after a configured idle interval the daemon kills the agent's claude process + tmux session (freeing resources) while preserving its workspace and SessionID, and auto-resumes it on the next incoming message.

**Architecture:** A new `Suspended` flag on the agentstore record (parallel to the existing `Stopped` flag) marks an agent as resumable-dormant. A daemon-side sweep goroutine polls tmux `session_activity`/`session_attached` and calls `Manager.Suspend` for agents idle past their interval (never while attached). The web message handler (`leo_send_message` path) detects a suspended target and calls `Manager.Resume` — which re-spawns with `--resume <SessionID>` — before delivering via the readiness-probing `tmux.InjectPrompt`. Interval is configured via a full cascade (`defaults.idle_suspend_after` → `templates.<name>.idle_suspend_after` → `--idle-suspend` spawn flag), resolved at spawn time and stored on the record.

**Tech Stack:** Go, tmux (control via `tmux.Args`/`tmux.Target`), cobra (CLI), net/http (daemon IPC + web), YAML config. Tests: standard `go test` table-driven, `-race`.

**Spec:** `docs/superpowers/specs/2026-06-19-agent-idle-suspend-design.md`

**Conventions in this codebase:**
- Run a single test: `go test -race -run TestName ./internal/<pkg>/`
- Full check before done: `make test && make lint`
- Commit per task (conventional commits, `feat:`/`refactor:`/`test:`).
- Package-level `var execCommand = exec.CommandContext` style seams are replaced in tests.

---

## File map

| File | Change |
|---|---|
| `internal/config/config.go` | Add `IdleSuspendAfter` to `DefaultsConfig` + `TemplateConfig`; validate; add `ResolveIdleSuspend` resolver |
| `internal/agentstore/store.go` | Add `Suspended bool` + `IdleSuspendAfter string` to `Record` |
| `internal/tmux/activity.go` *(new)* | `ListSessionActivity` + `parseSessionActivity` + `SessionActivity` |
| `internal/agent/resume.go` *(new)* | `ResumeArgs` (moved from `service.argsWithResume`) |
| `internal/service/agents.go` | Use `agent.ResumeArgs`; skip `Suspended` records in `RestoreAgents` |
| `internal/agent/manager.go` | `SpawnSpec.IdleSuspend`; persist resolved interval; `Suspend`, `Resume`; surface suspended in `List` |
| `internal/service/sweep.go` *(new)* | `runIdleSweep`, `sweepIdleAgents`, `shouldSuspend`, `parseIdle` |
| `internal/service/process.go` | Start sweep goroutine in `RunSupervised` |
| `internal/daemon/types.go` | `AgentSpawnRequest.IdleSuspend` |
| `internal/daemon/server.go` | `AgentManager` interface + routes for suspend/resume |
| `internal/daemon/handlers_agents.go` | `handleAgentSuspend`, `handleAgentResume`; thread `IdleSuspend` in spawn |
| `internal/daemon/client_agents.go` | `AgentSuspend`, `AgentResume` clients |
| `internal/web/web.go` | Add `Resume` to `AgentService` interface |
| `internal/web/handlers.go` | Auto-wake suspended agent in `handleProcessMessage` |
| `internal/cli/agent.go` | `leo agent suspend`/`resume` subcommands; `--idle-suspend` spawn flag |
| `docs/configuration/` | Document `idle_suspend_after` |

---

## Task 1: Config — `IdleSuspendAfter` field, validation, resolver

**Files:**
- Modify: `internal/config/config.go` (`DefaultsConfig` ~182-194, `TemplateConfig` ~260-277, `Validate()` ~near 564, add resolver near `ProcessStaleResume` ~354)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/config/config_test.go`:

```go
func TestResolveIdleSuspendCascade(t *testing.T) {
	cfg := &Config{Defaults: DefaultsConfig{IdleSuspendAfter: "24h"}}
	tmpl := TemplateConfig{}

	// defaults only
	if got := cfg.ResolveIdleSuspend(tmpl, ""); got != 24*time.Hour {
		t.Fatalf("defaults: got %v, want 24h", got)
	}
	// template overrides defaults
	tmpl.IdleSuspendAfter = "30m"
	if got := cfg.ResolveIdleSuspend(tmpl, ""); got != 30*time.Minute {
		t.Fatalf("template: got %v, want 30m", got)
	}
	// per-spawn override wins
	if got := cfg.ResolveIdleSuspend(tmpl, "2h"); got != 2*time.Hour {
		t.Fatalf("override: got %v, want 2h", got)
	}
	// unset everywhere => disabled (0)
	if got := (&Config{}).ResolveIdleSuspend(TemplateConfig{}, ""); got != 0 {
		t.Fatalf("unset: got %v, want 0", got)
	}
	// unparseable => disabled (0), not a panic
	if got := (&Config{Defaults: DefaultsConfig{IdleSuspendAfter: "garbage"}}).ResolveIdleSuspend(TemplateConfig{}, ""); got != 0 {
		t.Fatalf("garbage: got %v, want 0", got)
	}
}

func TestValidateRejectsBadIdleSuspend(t *testing.T) {
	cfg := &Config{
		Defaults:  DefaultsConfig{Model: "sonnet", IdleSuspendAfter: "nope"},
		Templates: map[string]TemplateConfig{"t": {IdleSuspendAfter: "5x"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad durations")
	}
	if !strings.Contains(err.Error(), "idle_suspend_after") {
		t.Fatalf("error should mention idle_suspend_after: %v", err)
	}
}
```

(Ensure `time` and `strings` are imported in the test file.)

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run 'TestResolveIdleSuspend|TestValidateRejectsBadIdleSuspend' ./internal/config/`
Expected: FAIL — `cfg.ResolveIdleSuspend` undefined.

- [ ] **Step 3: Add struct fields**

In `DefaultsConfig` (after `StaleResumeHours`):

```go
	// IdleSuspendAfter, when set to a Go duration (e.g. "24h", "30m"), is the
	// global default idle interval after which an ephemeral agent is suspended
	// (process + tmux killed, conversation preserved for auto-resume). Empty
	// disables idle-suspend. Overridable per template and per spawn.
	IdleSuspendAfter string `yaml:"idle_suspend_after,omitempty"`
```

In `TemplateConfig` (after `PermissionMode`):

```go
	// IdleSuspendAfter overrides defaults.idle_suspend_after for agents spawned
	// from this template. A Go duration ("24h"); empty inherits the default.
	IdleSuspendAfter string `yaml:"idle_suspend_after,omitempty"`
```

- [ ] **Step 4: Add resolver** (place next to `ProcessStaleResume`)

```go
// ResolveIdleSuspend returns the effective idle-suspend interval for an agent
// spawned from tmpl, with an optional per-spawn override. Cascade:
// override → template → defaults. An empty, unparseable, or non-positive value
// at the winning level means idle-suspend is disabled (returns 0).
func (c *Config) ResolveIdleSuspend(tmpl TemplateConfig, override string) time.Duration {
	raw := c.Defaults.IdleSuspendAfter
	if tmpl.IdleSuspendAfter != "" {
		raw = tmpl.IdleSuspendAfter
	}
	if override != "" {
		raw = override
	}
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
```

- [ ] **Step 5: Add validation** in `Validate()`

Near where `defaults` model is validated, add:

```go
	if c.Defaults.IdleSuspendAfter != "" {
		if _, err := time.ParseDuration(c.Defaults.IdleSuspendAfter); err != nil {
			errs = append(errs, fmt.Sprintf("defaults.idle_suspend_after %q is not a valid duration: %v", c.Defaults.IdleSuspendAfter, err))
		}
	}
```

In the loop over `c.Templates` (if no such loop exists yet, add `for name, tmpl := range c.Templates { ... }` near the other map-validation loops), add — mirroring the existing `sess.IdleTimeout` block:

```go
		if tmpl.IdleSuspendAfter != "" {
			if _, err := time.ParseDuration(tmpl.IdleSuspendAfter); err != nil {
				errs = append(errs, fmt.Sprintf("templates.%s.idle_suspend_after %q is not a valid duration: %v", name, tmpl.IdleSuspendAfter, err))
			}
		}
```

(`errs` is the existing `[]string` accumulator returned as a joined error — match the exact return idiom already in `Validate()`.)

- [ ] **Step 6: Run — verify PASS**

Run: `go test -race -run 'TestResolveIdleSuspend|TestValidateRejectsBadIdleSuspend' ./internal/config/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: idle_suspend_after config field, validation, and cascade resolver"
```

---

## Task 2: agentstore — `Suspended` + `IdleSuspendAfter` record fields

**Files:**
- Modify: `internal/agentstore/store.go` (`Record` struct ~33-55)
- Test: `internal/agentstore/store_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestRecordRoundTripPreservesSuspendFields(t *testing.T) {
	home := t.TempDir()
	in := Record{Name: "leo-x", Workspace: "/w", Suspended: true, IdleSuspendAfter: "24h0m0s"}
	if err := Save(home, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(FilePath(home))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rec := got["leo-x"]
	if !rec.Suspended || rec.IdleSuspendAfter != "24h0m0s" {
		t.Fatalf("round-trip lost fields: %+v", rec)
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestRecordRoundTripPreservesSuspendFields ./internal/agentstore/`
Expected: FAIL — `Suspended`/`IdleSuspendAfter` unknown fields.

- [ ] **Step 3: Add fields** to `Record` (after the `Stopped` field, before `NoResume`)

```go
	// Suspended marks an agent that the daemon idle-suspended: its process and
	// tmux session were killed to free resources, but the record (and
	// SessionID) is preserved so the conversation auto-resumes on the next
	// incoming message. Distinct from Stopped (user-initiated, terminal):
	// RestoreAgents skips Suspended records (no boot-time respawn) and Prune
	// keys off Stopped, so suspended worktrees are never pruned.
	Suspended bool `json:"suspended,omitempty"`

	// IdleSuspendAfter is the resolved idle interval (a Go duration string)
	// stamped at spawn time from the config cascade. The idle sweep reads this
	// off the record rather than re-resolving config, so behavior is stable
	// across config edits and daemon restarts. Empty means idle-suspend is off.
	IdleSuspendAfter string `json:"idle_suspend_after,omitempty"`
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestRecordRoundTripPreservesSuspendFields ./internal/agentstore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentstore/store.go internal/agentstore/store_test.go
git commit -m "feat: agentstore record carries Suspended + IdleSuspendAfter"
```

---

## Task 3: tmux — `ListSessionActivity` activity probe

**Files:**
- Create: `internal/tmux/activity.go`
- Test: `internal/tmux/activity_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/tmux/activity_test.go`:

```go
package tmux

import (
	"testing"
	"time"
)

func TestParseSessionActivity(t *testing.T) {
	out := "leo-a|0|1700000000\nleo-b|2|1700000600\nmalformed-line\nleo-c|x|notanumber\n"
	got := parseSessionActivity(out)

	if len(got) != 2 {
		t.Fatalf("want 2 valid sessions, got %d: %+v", len(got), got)
	}
	a, ok := got["leo-a"]
	if !ok || a.Attached != 0 || !a.LastActivity.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("leo-a parsed wrong: %+v ok=%v", a, ok)
	}
	b := got["leo-b"]
	if b.Attached != 2 || !b.LastActivity.Equal(time.Unix(1700000600, 0)) {
		t.Fatalf("leo-b parsed wrong: %+v", b)
	}
	if _, bad := got["leo-c"]; bad {
		t.Fatal("leo-c had unparseable epoch and should be skipped")
	}
}

func TestParseSessionActivityEmpty(t *testing.T) {
	if got := parseSessionActivity("\n  \n"); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestParseSessionActivity ./internal/tmux/`
Expected: FAIL — `parseSessionActivity` undefined.

- [ ] **Step 3: Implement** `internal/tmux/activity.go`

```go
package tmux

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// activityExecCommand is the seam tests replace.
var activityExecCommand = exec.CommandContext

// SessionActivity is the liveness metadata the idle-suspend sweep needs for one
// tmux session: how many clients are attached and when the session was last
// active (tmux's session_activity, which advances on injected input,
// interactive typing in an attached pane, and the pane's own output).
type SessionActivity struct {
	Attached     int
	LastActivity time.Time
}

// ListSessionActivity returns per-session activity for every session on Leo's
// tmux server, keyed by session name. One `list-sessions` call serves a whole
// sweep. A dead/absent server ("no server running") yields an empty map and a
// nil error — the sweep treats that as "nothing to suspend".
func ListSessionActivity(ctx context.Context, tmuxPath string) (map[string]SessionActivity, error) {
	const format = "#{session_name}|#{session_attached}|#{session_activity}"
	out, err := activityExecCommand(ctx, tmuxPath, Args("list-sessions", "-F", format)...).Output()
	if err != nil {
		// `tmux list-sessions` exits non-zero when no server is running.
		// Best-effort: report no sessions rather than an error.
		return map[string]SessionActivity{}, nil
	}
	return parseSessionActivity(string(out)), nil
}

// parseSessionActivity parses the `name|attached|epoch` lines emitted by
// ListSessionActivity. Malformed lines and unparseable epochs are skipped.
func parseSessionActivity(out string) map[string]SessionActivity {
	result := make(map[string]SessionActivity)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		epoch, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil {
			continue
		}
		attached, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		result[parts[0]] = SessionActivity{
			Attached:     attached,
			LastActivity: time.Unix(epoch, 0),
		}
	}
	return result
}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestParseSessionActivity ./internal/tmux/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tmux/activity.go internal/tmux/activity_test.go
git commit -m "feat: tmux ListSessionActivity probe for idle detection"
```

---

## Task 4: Share resume-arg rewriting — `agent.ResumeArgs`

The existing `service.argsWithResume` is needed by both `RestoreAgents` and the new `Manager.Resume`. Move it into the `agent` package (which owns claude-arg construction) and have `service` call it. DRY.

**Files:**
- Create: `internal/agent/resume.go`
- Modify: `internal/service/agents.go` (delete local `argsWithResume`, call `agent.ResumeArgs`)
- Test: `internal/agent/resume_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/agent/resume_test.go`:

```go
package agent

import (
	"reflect"
	"testing"
)

func TestResumeArgsStripsAndAppends(t *testing.T) {
	in := []string{"--model", "sonnet", "--session-id", "old", "--name", "leo-x"}
	got := ResumeArgs(in, "new")
	want := []string{"--model", "sonnet", "--name", "leo-x", "--resume", "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResumeArgsEmptySessionStripsOnly(t *testing.T) {
	in := []string{"--resume", "old", "--model", "opus"}
	got := ResumeArgs(in, "")
	want := []string{"--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestResumeArgs ./internal/agent/`
Expected: FAIL — `ResumeArgs` undefined.

- [ ] **Step 3: Implement** `internal/agent/resume.go`

```go
package agent

// ResumeArgs rewrites stored claude args so a restored or resumed agent rejoins
// a prior session. Any existing `--session-id`/`--resume` pair is stripped
// (defensive: never pass two session-selection flags) before appending
// `--resume <sessionID>`. An empty sessionID returns the args with those flags
// stripped — the caller has chosen a fresh spawn.
func ResumeArgs(args []string, sessionID string) []string {
	cleaned := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if (args[i] == "--session-id" || args[i] == "--resume") && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		cleaned = append(cleaned, args[i])
	}
	if sessionID == "" {
		return cleaned
	}
	return append(cleaned, "--resume", sessionID)
}
```

- [ ] **Step 4: Replace `service.argsWithResume`**

In `internal/service/agents.go`, delete the `argsWithResume` function (lines ~144-163) and change its one call site (~line 116) from:

```go
		args := argsWithResume(rec.ClaudeArgs, resumeID)
```
to:
```go
		args := agent.ResumeArgs(rec.ClaudeArgs, resumeID)
```

(`agent` is already imported in `agents.go`.) If a `TestArgsWithResume` test exists in `internal/service`, move/delete it — it's now covered in `internal/agent`.

- [ ] **Step 5: Run — verify PASS + no breakage**

Run: `go test -race ./internal/agent/ ./internal/service/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/resume.go internal/agent/resume_test.go internal/service/agents.go
git commit -m "refactor: move argsWithResume into agent.ResumeArgs (shared by restore + resume)"
```

---

## Task 5: Persist resolved idle interval at spawn

**Files:**
- Modify: `internal/agent/manager.go` (`SpawnSpec` ~72-85, `spawnShared` ~172-234, `spawnWorktree` ~249-349)
- Test: `internal/agent/manager_test.go` (use the existing fake supervisor pattern)

- [ ] **Step 1: Write failing test**

Find the existing fake `Supervisor` used in `manager_test.go` (it implements `ReserveAgent/ReleaseAgent/SpawnAgent/StopAgent/RenameAgent/EphemeralAgents`). Add a test that spawns with a template carrying `idle_suspend_after` and asserts the saved agentstore record stamped the resolved interval:

```go
func TestSpawnStampsResolvedIdleSuspend(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home, IdleSuspendAfter: "24h"},
		},
	}
	m := New(func() (*config.Config, error) { return cfg, nil }, newFakeSup(), "", "tok")

	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	for _, r := range recs {
		if r.IdleSuspendAfter != (24 * time.Hour).String() {
			t.Fatalf("idle interval not stamped: %q", r.IdleSuspendAfter)
		}
	}

	// per-spawn override beats the template
	for k := range recs {
		agentstore.Remove(home, k)
	}
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo2", IdleSuspend: "15m"}); err != nil {
		t.Fatalf("spawn override: %v", err)
	}
	recs, _ = agentstore.Load(agentstore.FilePath(home))
	for _, r := range recs {
		if r.IdleSuspendAfter != (15 * time.Minute).String() {
			t.Fatalf("override not applied: %q", r.IdleSuspendAfter)
		}
	}
}
```

Adjust `config.Config`/`SpawnSpec` literals to match the real constructors (e.g. if `ResolveWorkspace` needs a real workspace, point `Workspace` at `home`). If the existing fake supervisor's `SpawnAgent` records calls, reuse it; otherwise a minimal fake that returns nil is fine since the assertion is on the persisted record.

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestSpawnStampsResolvedIdleSuspend ./internal/agent/`
Expected: FAIL — `SpawnSpec.IdleSuspend` undefined.

- [ ] **Step 3: Add `SpawnSpec.IdleSuspend`** (after `Env` in `SpawnSpec`)

```go
	// IdleSuspend, when non-empty, overrides the template/defaults
	// idle_suspend_after for this spawn only (a Go duration like "24h").
	IdleSuspend string
```

- [ ] **Step 4: Stamp the resolved interval in `spawnShared`**

In `spawnShared`, after `env := mergeEnv(tmpl.Env, spec.Env)` and before the `agentstore.Save`, compute the interval:

```go
	idleStr := ""
	if d := cfg.ResolveIdleSuspend(tmpl, spec.IdleSuspend); d > 0 {
		idleStr = d.String()
	}
```

Then add `IdleSuspendAfter: idleStr,` to the `agentstore.Record{...}` literal in the `agentstore.Save` call.

- [ ] **Step 5: Stamp the resolved interval in `spawnWorktree`**

Identical: after `env := mergeEnv(tmpl.Env, spec.Env)` (before the worktree `agentstore.Save`), add the same `idleStr` block, and add `IdleSuspendAfter: idleStr,` to that `agentstore.Record{...}` literal.

- [ ] **Step 6: Run — verify PASS**

Run: `go test -race -run TestSpawnStampsResolvedIdleSuspend ./internal/agent/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/manager.go internal/agent/manager_test.go
git commit -m "feat: stamp resolved idle-suspend interval on agent record at spawn"
```

---

## Task 6: `Manager.Suspend`

**Files:**
- Modify: `internal/agent/manager.go` (add method near `Stop` ~425)
- Test: `internal/agent/manager_test.go`

- [ ] **Step 1: Write failing test**

The fake supervisor must report the agent as live (in `EphemeralAgents`) and record `StopAgent` calls. Add:

```go
func TestSuspendMarksRecordAndStops(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := newFakeSup()
	sup.live["leo-x"] = ProcessState{Name: "leo-x", Status: "running"}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Suspend("leo-x"); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	if !sup.stopped["leo-x"] {
		t.Fatal("StopAgent was not called")
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if !recs["leo-x"].Suspended {
		t.Fatal("record not marked Suspended")
	}
	if recs["leo-x"].SessionID != "sid" {
		t.Fatal("SessionID must be preserved for resume")
	}

	// not-running => error
	if err := m.Suspend("ghost"); err == nil {
		t.Fatal("suspending a non-running agent should error")
	}
}
```

Extend the fake supervisor as needed (add a `live map[string]ProcessState` returned by `EphemeralAgents()` and a `stopped map[string]bool` set by `StopAgent`). If `newFakeSup` doesn't exist, create a small fake in the test file implementing the `agent.Supervisor` interface.

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestSuspendMarksRecordAndStops ./internal/agent/`
Expected: FAIL — `m.Suspend` undefined.

- [ ] **Step 3: Implement `Suspend`** (add after `Stop`)

```go
// Suspend stops a running ephemeral agent's process and tmux session while
// preserving its agentstore record (Suspended=true) and SessionID, so the
// conversation auto-resumes on the next incoming message. The record is marked
// before the process is stopped; a failed stop rolls the flag back so the
// record never claims "suspended" while the process is still running. Returns
// an error when the agent is not currently running or has no persisted record.
func (m *Manager) Suspend(name string) error {
	if _, ok := m.sup.EphemeralAgents()[name]; !ok {
		return fmt.Errorf("agent %q is not running", name)
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok {
		return fmt.Errorf("no agentstore record for %q (cannot suspend an unpersisted agent)", name)
	}

	rec.Suspended = true
	rec.NoResume = false
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		return fmt.Errorf("marking agent suspended: %w", err)
	}

	if err := m.sup.StopAgent(name); err != nil {
		rec.Suspended = false
		if rbErr := agentstore.Save(cfg.HomePath, rec); rbErr != nil {
			log.Printf("agent %q: stop failed (%v) AND suspend-flag rollback failed (%v)", name, err, rbErr)
		}
		return fmt.Errorf("stopping agent for suspend: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestSuspendMarksRecordAndStops ./internal/agent/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go internal/agent/manager_test.go
git commit -m "feat: Manager.Suspend — kill process, preserve resumable record"
```

---

## Task 7: `Manager.Resume`

**Files:**
- Modify: `internal/agent/manager.go` (add method near `Suspend`)
- Test: `internal/agent/manager_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestResumeRespawnsWithResumeAndClearsFlag(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := newFakeSup()
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Workspace: "/w", SessionID: "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Suspended:  true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	rec, err := m.Resume("leo-x")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rec.Status != "starting" {
		t.Fatalf("want starting, got %q", rec.Status)
	}

	// spawned with --resume sid, no --session-id
	got := sup.lastSpawn.ClaudeArgs
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Fatalf("resume args wrong: %v", got)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].Suspended {
		t.Fatal("Suspended flag not cleared after resume")
	}

	// resuming a non-suspended/unknown agent errors
	if _, err := m.Resume("ghost"); err == nil {
		t.Fatal("resuming unknown agent should error")
	}
}
```

Add `lastSpawn agent.SpawnRequest` capture to the fake supervisor's `SpawnAgent`, and small `containsPair`/`containsFlag` helpers if not present. `session.LatestSession("/w", 0)` will error for a nonexistent dir, so `Resume` falls back to `rec.SessionID` ("sid") — exactly what this test asserts.

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestResumeRespawnsWithResumeAndClearsFlag ./internal/agent/`
Expected: FAIL — `m.Resume` undefined.

- [ ] **Step 3: Implement `Resume`** (add after `Suspend`)

```go
// Resume restarts a suspended ephemeral agent, rejoining its prior claude
// session via --resume. If the agent is already running it is a no-op that
// returns the live record. Errors when name has no suspended record.
//
// Mirrors RestoreAgents' resume logic: prefer the newest jsonl in the
// workspace over the stored SessionID (catches /clear sessions the store never
// saw), then spawn with ResumeArgs and clear the Suspended flag. The stored
// ClaudeArgs are left untouched (still carrying --session-id); a future restore
// rebuilds resume args from them + the SessionID, matching existing behavior.
func (m *Manager) Resume(name string) (Record, error) {
	if st, ok := m.sup.EphemeralAgents()[name]; ok {
		r := Record{Name: name, Status: st.Status, StartedAt: st.StartedAt, Restarts: st.Restarts}
		if cfg, err := m.cfgLoader(); err == nil {
			if stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath)); err == nil {
				mergeStored(&r, stored)
			}
		}
		return r, nil
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return Record{}, fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok || !rec.Suspended {
		return Record{}, fmt.Errorf("agent %q is not suspended", name)
	}

	resumeID := rec.SessionID
	if latestID, _, err := session.LatestSession(rec.Workspace, 0); err == nil && latestID != "" {
		resumeID = latestID
	}
	args := ResumeArgs(rec.ClaudeArgs, resumeID)

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       rec.Name,
		ClaudeArgs: args,
		WorkDir:    rec.Workspace,
		Env:        rec.Env,
		WebPort:    rec.WebPort,
		WebToken:   m.webToken,
	}); err != nil {
		return Record{}, fmt.Errorf("respawning suspended agent: %w", err)
	}

	rec.Suspended = false
	rec.SessionID = resumeID
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		log.Printf("agent %q resumed but agentstore.Save failed: %v — flag may persist until next save", rec.Name, err)
	}

	return Record{
		Name:          rec.Name,
		Template:      rec.Template,
		Repo:          rec.Repo,
		Workspace:     rec.Workspace,
		Branch:        rec.Branch,
		CanonicalPath: rec.CanonicalPath,
		Status:        "starting",
		StartedAt:     time.Now(),
		Env:           rec.Env,
	}, nil
}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestResumeRespawnsWithResumeAndClearsFlag ./internal/agent/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go internal/agent/manager_test.go
git commit -m "feat: Manager.Resume — respawn suspended agent via --resume"
```

---

## Task 8: Surface suspended agents in `Manager.List`

**Files:**
- Modify: `internal/agent/manager.go` (`List` ~372-417)
- Test: `internal/agent/manager_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestListSurfacesSuspendedAgents(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := newFakeSup() // no live agents
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-shared", Workspace: "/w", Suspended: true})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-wt", Workspace: "/w2", Branch: "feat", Suspended: true})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	got := m.List()

	statuses := map[string]string{}
	for _, r := range got {
		statuses[r.Name] = r.Status
	}
	if statuses["leo-shared"] != "suspended" {
		t.Fatalf("shared suspended agent missing/wrong: %v", statuses)
	}
	if statuses["leo-wt"] != "suspended" {
		t.Fatalf("worktree suspended agent missing/wrong: %v", statuses)
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestListSurfacesSuspendedAgents ./internal/agent/`
Expected: FAIL — shared suspended agent dropped (current code skips non-worktree stored records).

- [ ] **Step 3: Implement** — in `List`, inside the `for name, rec := range stored` loop, add a `Suspended` branch BEFORE the `if rec.Branch == "" { continue }` line:

```go
		if rec.Suspended {
			out = append(out, Record{
				Name:          name,
				Template:      rec.Template,
				Repo:          rec.Repo,
				Workspace:     rec.Workspace,
				Branch:        rec.Branch,
				CanonicalPath: rec.CanonicalPath,
				Status:        "suspended",
				StartedAt:     rec.SpawnedAt,
				Env:           rec.Env,
			})
			continue
		}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestListSurfacesSuspendedAgents ./internal/agent/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go internal/agent/manager_test.go
git commit -m "feat: List surfaces suspended agents (shared + worktree) as status=suspended"
```

---

## Task 9: `RestoreAgents` skips suspended records

**Files:**
- Modify: `internal/service/agents.go` (`RestoreAgents` ~66-69)
- Test: `internal/service/agents_test.go`

- [ ] **Step 1: Write failing test**

Mirror the existing `Stopped`-skip test. The fake `agentSpawner` records which names it was asked to spawn:

```go
func TestRestoreAgentsSkipsSuspended(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-live", Workspace: home, SessionID: "a"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-susp", Workspace: home, SessionID: "b", Suspended: true})

	fake := &fakeSpawner{} // implements agentSpawner; collects spec.Name into a slice
	RestoreAgents(home, "", "tok", fake)

	if fake.spawned["leo-susp"] {
		t.Fatal("suspended agent must not be respawned at boot")
	}
	if !fake.spawned["leo-live"] {
		t.Fatal("non-suspended agent should be restored")
	}
}
```

Reuse or extend the existing `agentSpawner` fake in `agents_test.go`. Point `Workspace` at an existing dir (`home`) so the worktree-missing branch doesn't drop the records (these are shared-workspace records — `Branch==""` — so the `os.Stat` check is skipped anyway, but keep it valid).

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestRestoreAgentsSkipsSuspended ./internal/service/`
Expected: FAIL — suspended agent is respawned.

- [ ] **Step 3: Implement** — in `RestoreAgents`, right after the existing `if rec.Stopped { ... continue }` block, add:

```go
		if rec.Suspended {
			// Daemon idle-suspended this agent. Keep the record; auto-wake on
			// the next incoming message resumes it. Do not resurrect at boot.
			continue
		}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run TestRestoreAgentsSkipsSuspended ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/agents.go internal/service/agents_test.go
git commit -m "feat: RestoreAgents leaves suspended agents dormant across daemon restart"
```

---

## Task 10: Idle-suspend sweep loop

**Files:**
- Create: `internal/service/sweep.go`
- Modify: `internal/service/process.go` (`RunSupervised`/`defaultSupervisedExec` — start the goroutine after the agent manager is built, ~line 496)
- Test: `internal/service/sweep_test.go`

- [ ] **Step 1: Write failing test** (pure decision function — no tmux needed)

Create `internal/service/sweep_test.go`:

```go
package service

import (
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestShouldSuspend(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	idle := 30 * time.Minute

	cases := []struct {
		name string
		act  tmux.SessionActivity
		idle time.Duration
		want bool
	}{
		{"idle past threshold, detached", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-31 * time.Minute)}, idle, true},
		{"idle under threshold", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-29 * time.Minute)}, idle, false},
		{"attached blocks suspend", tmux.SessionActivity{Attached: 1, LastActivity: now.Add(-2 * time.Hour)}, idle, false},
		{"disabled interval", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-2 * time.Hour)}, 0, false},
		{"exactly at threshold suspends", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-30 * time.Minute)}, idle, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldSuspend(now, c.act, c.idle); got != c.want {
				t.Fatalf("shouldSuspend = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseIdle(t *testing.T) {
	if parseIdle("") != 0 || parseIdle("bad") != 0 || parseIdle("-5m") != 0 {
		t.Fatal("invalid/empty/negative durations must parse to 0")
	}
	if parseIdle("24h") != 24*time.Hour {
		t.Fatal("24h should parse")
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run 'TestShouldSuspend|TestParseIdle' ./internal/service/`
Expected: FAIL — `shouldSuspend`/`parseIdle` undefined.

- [ ] **Step 3: Implement** `internal/service/sweep.go`

```go
package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// idleSweepInterval is how often the daemon checks live agents for idleness.
// A package var so tests can shorten it.
var idleSweepInterval = 60 * time.Second

// runIdleSweep periodically suspends ephemeral agents that have been idle past
// their configured interval. It runs for the daemon's lifetime; ctx cancellation
// (shutdown) stops it.
func runIdleSweep(ctx context.Context, sup *Supervisor, mgr *agent.Manager, tmuxPath, homePath string) {
	t := time.NewTicker(idleSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sweepIdleAgents(ctx, sup, mgr, tmuxPath, homePath)
	}
}

// sweepIdleAgents runs a single sweep pass: for each live ephemeral agent with a
// configured idle interval, suspend it if its tmux session has been inactive
// long enough and no client is attached.
func sweepIdleAgents(ctx context.Context, sup *Supervisor, mgr *agent.Manager, tmuxPath, homePath string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil || len(records) == 0 {
		return
	}
	activity, err := tmux.ListSessionActivity(ctx, tmuxPath)
	if err != nil {
		return
	}
	now := time.Now()
	for name := range sup.EphemeralAgents() {
		rec, ok := records[name]
		if !ok {
			continue
		}
		idle := parseIdle(rec.IdleSuspendAfter)
		if idle <= 0 {
			continue
		}
		act, ok := activity[agent.SessionName(name)]
		if !ok {
			continue // no tmux session metadata — leave it alone
		}
		if shouldSuspend(now, act, idle) {
			if err := mgr.Suspend(name); err != nil {
				fmt.Fprintf(os.Stderr, "idle-sweep: suspend %q failed: %v\n", name, err)
			} else {
				fmt.Fprintf(os.Stdout, "idle-sweep: suspended %q (idle >= %s)\n", name, idle)
			}
		}
	}
}

// shouldSuspend reports whether an agent should be suspended now: idle-suspend
// must be enabled, no client attached, and the session inactive for at least the
// idle interval.
func shouldSuspend(now time.Time, act tmux.SessionActivity, idle time.Duration) bool {
	if idle <= 0 || act.Attached > 0 {
		return false
	}
	return now.Sub(act.LastActivity) >= idle
}

// parseIdle parses a stored idle-interval string. Empty, invalid, or
// non-positive values mean "disabled" (0).
func parseIdle(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
```

- [ ] **Step 4: Run — verify PASS**

Run: `go test -race -run 'TestShouldSuspend|TestParseIdle' ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Wire the goroutine into `defaultSupervisedExec`**

In `internal/service/process.go`, inside the `else` branch where the agent manager is built, right after `srv.SetAgentManager(agentMgr)` (line ~496), add:

```go
			// Idle-suspend sweep: suspends ephemeral agents that have gone idle
			// past their configured interval (see Manager.Suspend). Runs for the
			// daemon's lifetime; ctx cancellation stops it.
			go runIdleSweep(ctx, supervisor, agentMgr, tmuxPath, homePath)
```

- [ ] **Step 6: Run — full service build + tests**

Run: `go build ./... && go test -race ./internal/service/`
Expected: builds; PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/service/sweep.go internal/service/sweep_test.go internal/service/process.go
git commit -m "feat: daemon idle-suspend sweep loop"
```

---

## Task 11: Daemon — suspend/resume handlers, interface, routes, clients

**Files:**
- Modify: `internal/daemon/server.go` (`AgentManager` interface ~34-44, routes in `New` ~near the `/agents/...` block)
- Modify: `internal/daemon/handlers_agents.go` (new handlers; thread `IdleSuspend` in `handleAgentSpawn`)
- Modify: `internal/daemon/types.go` (`AgentSpawnRequest.IdleSuspend`)
- Modify: `internal/daemon/client_agents.go` (`AgentSuspend`, `AgentResume`)
- Test: `internal/daemon/handlers_agents_test.go`

- [ ] **Step 1: Write failing test** (mirror the existing agent-handler tests, which use a fake `AgentManager`)

```go
func TestHandleAgentSuspendAndResume(t *testing.T) {
	fake := &fakeAgentManager{ // extend existing fake to record Suspend + impl Resume
		resumeRec: agent.Record{Name: "leo-x", Status: "starting"},
	}
	srv := newTestServerWithAgentMgr(t, fake) // use whatever harness the existing tests use

	// suspend
	rr := doRequest(t, srv, "POST", "/agents/leo-x/suspend", nil)
	if rr.Code != http.StatusOK || !fake.suspended["leo-x"] {
		t.Fatalf("suspend failed: code=%d suspended=%v", rr.Code, fake.suspended)
	}

	// resume
	rr = doRequest(t, srv, "POST", "/agents/leo-x/resume", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("resume code=%d", rr.Code)
	}
	var resp Response
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.OK {
		t.Fatalf("resume not OK: %s", resp.Error)
	}
}
```

Match the existing test helpers in `handlers_agents_test.go` (request helper, fake manager constructor). Add `Suspend(name string) error` and `Resume(name string) (agent.Record, error)` to the test's fake `AgentManager` implementation.

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestHandleAgentSuspendAndResume ./internal/daemon/`
Expected: FAIL — methods/routes missing.

- [ ] **Step 3: Extend the `AgentManager` interface** (`server.go`), add:

```go
	Suspend(name string) error
	Resume(name string) (agent.Record, error)
```

- [ ] **Step 4: Register routes** in `New` (next to the other `/agents/{name}/...` routes):

```go
	mux.HandleFunc("POST /agents/{name}/suspend", s.handleAgentSuspend)
	mux.HandleFunc("POST /agents/{name}/resume", s.handleAgentResume)
```

- [ ] **Step 5: Implement handlers** in `handlers_agents.go`:

```go
// handleAgentSuspend drives agent.Manager.Suspend via POST /agents/{name}/suspend.
func (s *Server) handleAgentSuspend(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if err := s.agentMgr.Suspend(name); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentResume drives agent.Manager.Resume via POST /agents/{name}/resume.
func (s *Server) handleAgentResume(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	rec, err := s.agentMgr.Resume(name)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling record: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
```

- [ ] **Step 6: Thread `IdleSuspend` through spawn**

In `internal/daemon/types.go`, add to `AgentSpawnRequest` (after `Env`):

```go
	IdleSuspend string `json:"idle_suspend,omitempty"`
```

In `handleAgentSpawn` (`handlers_agents.go`), add `IdleSuspend: req.IdleSuspend,` to the `agent.SpawnSpec{...}` it builds.

- [ ] **Step 7: Add client functions** in `client_agents.go` (mirror `AgentStop`):

```go
// AgentSuspend sends POST /agents/{name}/suspend to the daemon.
func AgentSuspend(ctx context.Context, workDir, name string) error {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/suspend", nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return responseError(resp, name)
	}
	return nil
}

// AgentResume sends POST /agents/{name}/resume to the daemon and returns the
// resumed record.
func AgentResume(ctx context.Context, workDir, name string) (agent.Record, error) {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/resume", nil)
	if err != nil {
		return agent.Record{}, err
	}
	if !resp.OK {
		return agent.Record{}, responseError(resp, name)
	}
	var rec agent.Record
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		return agent.Record{}, fmt.Errorf("decoding resume response: %w", err)
	}
	return rec, nil
}
```

- [ ] **Step 8: Run — verify PASS + build**

Run: `go build ./... && go test -race ./internal/daemon/`
Expected: builds; PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/daemon/
git commit -m "feat: daemon suspend/resume endpoints + clients + IdleSuspend spawn field"
```

---

## Task 12: Web — auto-wake suspended agent on incoming message

When `leo_send_message` targets a suspended agent, the web handler resumes it, then delivers via the readiness-probing `tmux.InjectPrompt` (handles cold-start boot).

**Files:**
- Modify: `internal/web/web.go` (`AgentService` interface — add `Resume`)
- Modify: `internal/web/handlers.go` (`handleProcessMessage` ~824-885)
- Test: `internal/web/web_test.go`

- [ ] **Step 1: Write failing test**

Mirror the existing `handleProcessMessage` tests. Use a fake `AgentService` whose `Resume` returns a record for a known suspended name, and a fake `processes` provider whose `States()` does NOT contain that name (so the live path is skipped). Stub `s.execCommand` to capture tmux calls (or assert `Resume` was invoked + a 200 returned):

```go
func TestProcessMessageAutoWakesSuspendedAgent(t *testing.T) {
	s := newTestWebServer(t) // existing harness
	fakeAgents := &fakeAgentService{resumeOK: map[string]bool{"leo-x": true}}
	s.agentSvc = fakeAgents
	// processes.States() returns no "leo-x" => not live

	rr := postJSON(t, s, "/web/process/leo-x/message", map[string]string{"text": "hello"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rr.Code, rr.Body)
	}
	if !fakeAgents.resumed["leo-x"] {
		t.Fatal("suspended agent was not resumed")
	}
}

func TestProcessMessageUnknownTargetStill404s(t *testing.T) {
	s := newTestWebServer(t)
	s.agentSvc = &fakeAgentService{} // Resume errors for everything
	rr := postJSON(t, s, "/web/process/ghost/message", map[string]string{"text": "x"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}
```

Extend the web test's fake `AgentService` with `Resume(name string) (agent.Record, error)` (returns a record when `resumeOK[name]`, else an error) and a `resumed map[string]bool` it records into. Make sure `InjectPrompt`'s tmux calls are absorbed by the stubbed `execCommand` (or by `tmux.activityExecCommand`/`tmux.execCommand` seams) so the test doesn't shell out — if `InjectPrompt` is hard to stub here, have the suspended branch call `s.agentSvc.Resume` then fall through to the existing send-keys delivery (which already uses `s.execCommand`) instead of `InjectPrompt`; see Step 3 note.

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run 'TestProcessMessageAutoWakes|TestProcessMessageUnknownTarget' ./internal/web/`
Expected: FAIL — suspended target 404s today.

- [ ] **Step 3: Add `Resume` to the `AgentService` interface** (`web.go`, the interface near line 40-56):

```go
	Resume(name string) (agent.Record, error)
```

- [ ] **Step 4: Implement auto-wake** in `handleProcessMessage`

Replace the existing "validate target against running sessions" block (the `if _, ok := states[name]; !ok { ... 404 ... }`) with a version that tries resume first:

```go
	// Validate the target against running sessions (processes + agents).
	states := s.processes.States()
	if _, ok := states[name]; !ok {
		// Not live. If it's a suspended agent, auto-wake it and deliver via the
		// readiness-probing InjectPrompt (which waits out claude's cold boot).
		if s.agentSvc != nil {
			if rec, err := s.agentSvc.Resume(name); err == nil {
				sessionName := agent.SessionName(rec.Name)
				if err := tmux.InjectPrompt(r.Context(), findTmuxPath(), sessionName, req.Text); err != nil {
					writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("deliver after resume failed: %v", err)})
					return
				}
				writeJSON(w, http.StatusOK, apiResponse{OK: true})
				return
			}
		}
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
```

Leave the rest of `handleProcessMessage` (the live send-keys + Enter path) unchanged. Ensure `tmux` is imported in `handlers.go` (it already is, for `tmux.Args`/`tmux.PaneTarget`).

> Note: if stubbing `tmux.InjectPrompt` in the web test proves awkward, instead have the suspended branch resume then fall through to the existing live delivery path (it polls readiness via `waitForInputContent`). The InjectPrompt route is preferred for its longer cold-start budget; choose based on test ergonomics, but keep the resume-before-deliver ordering.

- [ ] **Step 5: Run — verify PASS**

Run: `go test -race -run 'TestProcessMessageAutoWakes|TestProcessMessageUnknownTarget' ./internal/web/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/web/web.go internal/web/handlers.go internal/web/web_test.go
git commit -m "feat: auto-wake suspended agent on incoming message"
```

---

## Task 13: CLI — `leo agent suspend`/`resume` + `--idle-suspend` spawn flag

**Files:**
- Modify: `internal/cli/agent.go` (`newAgentCmd` AddCommand ~62; new `newAgentSuspendCmd`/`newAgentResumeCmd`; `--idle-suspend` flag on `newAgentSpawnCmd` ~181 + into the `daemon.AgentSpawnRequest` ~328)
- Test: `internal/cli/agent_test.go` (smoke — command registration + flag wiring; the e2e path requires a daemon)

- [ ] **Step 1: Write failing test** (registration smoke)

```go
func TestAgentCmdRegistersSuspendResume(t *testing.T) {
	cmd := newAgentCmd()
	have := map[string]bool{}
	for _, c := range cmd.Commands() {
		have[c.Name()] = true
	}
	if !have["suspend"] || !have["resume"] {
		t.Fatalf("suspend/resume not registered: %v", have)
	}
}

func TestAgentSpawnHasIdleSuspendFlag(t *testing.T) {
	cmd := newAgentSpawnCmd()
	if cmd.Flags().Lookup("idle-suspend") == nil {
		t.Fatal("--idle-suspend flag not registered on spawn")
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run 'TestAgentCmdRegistersSuspendResume|TestAgentSpawnHasIdleSuspendFlag' ./internal/cli/`
Expected: FAIL.

- [ ] **Step 3: Add `--idle-suspend` to spawn**

In `newAgentSpawnCmd`, add a `var idleSuspend string` alongside the other flag vars, register it:

```go
	cmd.Flags().StringVar(&idleSuspend, "idle-suspend", "", "suspend this agent after this idle duration (e.g. 24h); empty uses template/defaults")
```

and pass it into the spawn request (line ~328):

```go
			rec, err := daemon.AgentSpawn(cmd.Context(), cfg.HomePath, daemon.AgentSpawnRequest{
				Template:    template,
				Repo:        repo,
				Name:        name,
				Branch:      branch,
				Base:        base,
				Prompt:      prompt,
				Env:         env,
				IdleSuspend: idleSuspend,
			})
```

Also add `idle-suspend` to the remote-forward arg assembly (the `if !res.Localhost { extra := ... }` block in spawn) so it works over SSH — append `"--idle-suspend", idleSuspend` when non-empty, matching how other spawn flags are forwarded.

- [ ] **Step 4: Add suspend/resume subcommands** (register in `newAgentCmd`'s `AddCommand(...)`: add `newAgentSuspendCmd(),` and `newAgentResumeCmd(),`). Model both on `newAgentStopCmd` (host dispatch + remote forwarding + `daemon.AgentResolve` to canonicalize):

```go
func newAgentSuspendCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:               "suspend <name>",
		Short:             "Suspend a running agent (free resources, keep it resumable)",
		Long:              `Suspend a running agent: its claude process and tmux session are killed to free resources, but its workspace and session are preserved. The agent auto-resumes on the next incoming message, or via 'leo agent resume'.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"suspend", name})
			}
			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			if err := daemon.AgentSuspend(cmd.Context(), cfg.HomePath, resolved.Name); err != nil {
				return fmt.Errorf("suspending agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "suspended %s\n", resolved.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

func newAgentResumeCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume a suspended agent",
		Long:  `Resume a suspended agent, rejoining its prior claude conversation via --resume.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"resume", name})
			}
			rec, err := daemon.AgentResume(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resuming agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "resumed %s\n", rec.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}
```

> Note on `resume` + resolve: `daemon.AgentResolve` only matches *live* agents, and a suspended agent is not live — so `resume` passes the raw name straight to `daemon.AgentResume`, which looks it up in agentstore by exact name. `suspend` operates on a live agent, so it resolves first (matching `stop`).

- [ ] **Step 5: Run — verify PASS + build**

Run: `go build ./... && go test -race -run 'TestAgentCmd|TestAgentSpawnHasIdle' ./internal/cli/`
Expected: builds; PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/agent.go internal/cli/agent_test.go
git commit -m "feat: leo agent suspend/resume commands + --idle-suspend spawn flag"
```

---

## Task 14: Documentation

**Files:**
- Modify: `docs/configuration/` (the file documenting `defaults` + `templates`; e.g. an `ephemeral-agents.md` or the main config reference — grep for `stale_resume_hours` to find where similar settings are documented)
- Modify: `CLAUDE.md` config section (the `defaults`/`templates` field lists)

- [ ] **Step 1: Document `idle_suspend_after`**

Find the docs page that lists `defaults`/`templates` fields:

Run: `grep -rl "stale_resume_hours\|idle_timeout" docs/`

Add a section describing:
- `defaults.idle_suspend_after: "24h"` — global default; empty/unset disables.
- `templates.<name>.idle_suspend_after` — per-template override.
- `leo agent spawn … --idle-suspend 24h` — per-spawn override.
- Semantics: after the interval with no tmux activity (and no attached client), the agent's process + tmux session are killed; the conversation is preserved and auto-resumes on the next message (or `leo agent resume`). Activity = tmux `session_activity` (injected prompts, interactive typing, claude output). An attached client blocks suspension.

- [ ] **Step 2: Update `CLAUDE.md`** — add `idle_suspend_after` to the `defaults` and `templates` field lists in the Config section.

- [ ] **Step 3: Commit**

```bash
git add docs/ CLAUDE.md
git commit -m "docs: document idle_suspend_after for ephemeral agents"
```

---

## Final verification

- [ ] **Run the full suite**

Run: `make test`
Expected: all packages PASS with `-race`.

- [ ] **Lint**

Run: `make lint`
Expected: clean (go vet + staticcheck).

- [ ] **Build**

Run: `make build`
Expected: `bin/leo` produced.

- [ ] **Manual smoke (optional, requires a running daemon)**
  1. Set a short interval on a template (`idle_suspend_after: "1m"`), reload config.
  2. Spawn an agent; confirm `leo agent list` shows it running.
  3. Wait > 1m without touching it; confirm the sweep logs `idle-sweep: suspended …` and `leo agent list` shows `suspended`.
  4. `leo_send_message` to it (or `leo agent resume <name>`); confirm it comes back and the message lands.
  5. Attach to a fresh agent and idle past the interval; confirm it is NOT suspended while attached.

---

## Spec coverage check

- State model (`suspended`, distinct from `stopped`) → Tasks 2, 8.
- Activity signal = tmux `session_activity`; attached guard → Tasks 3, 10.
- Sweep in daemon, 60s tick → Task 10.
- Auto-wake on message + readiness probe via InjectPrompt → Task 12.
- Resume reuses RestoreAgents' resume logic (shared `ResumeArgs`) → Tasks 4, 7.
- Manual suspend/resume (CLI) → Task 13.
- Config cascade (defaults/template/per-spawn), resolved at spawn, stored on record, validated → Tasks 1, 5.
- Daemon restart leaves suspended dormant → Task 9.
- Resume failure degrades via existing quick-exit `--resume → fresh` path → inherited from `superviseProcess` (no new work; noted in spec §5).
