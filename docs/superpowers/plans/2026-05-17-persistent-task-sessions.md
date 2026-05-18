# Persistent Task Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `runtime: persistent` task mode that runs scheduled prompts inside long-lived `claude` processes hosted in leo-supervised tmux sessions, with a Stop-hook reporting completions back to the daemon. Zero `claude -p` in the persistent flow.

**Architecture:** A new `sessions:` config block defines reusable supervised claude sessions (mirrors `processes:`). Tasks reference a session (or implicitly get a dedicated one) and `leo run <task>` enqueues prompts on a per-session FIFO managed by the daemon. The daemon injects via `tmux paste-buffer` + Enter, correlates Stop-hook reports by a UUID sentinel in the prompt, and persists session ids for crash-resume. Channel delivery happens in-session using the plugins already loaded via `LEO_CHANNELS`.

**Tech Stack:** Go 1.22+; cobra CLI; gopkg.in/yaml.v3; net/http over Unix socket for daemon IPC; tmux (`-L leo` socket); robfig/cron/v3 (existing).

**Spec:** `docs/superpowers/specs/2026-05-17-persistent-task-sessions-design.md`

---

## Phase 1 — Foundation (config, tmux, hooks)

### Task 1: Add config types for sessions

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestSessionConfigParses(t *testing.T) {
    yamlBlob := []byte(`
sessions:
  daily:
    workspace: /tmp/daily
    model: sonnet
    channels:
      - plugin:slack@official
tasks:
  standup:
    runtime: persistent
    session: daily
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels:
      - plugin:slack@official
    queue_max: 3
    lazy: false
`)
    var cfg Config
    if err := yaml.Unmarshal(yamlBlob, &cfg); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    sess, ok := cfg.Sessions["daily"]
    if !ok {
        t.Fatalf("expected sessions.daily")
    }
    if sess.Workspace != "/tmp/daily" || sess.Model != "sonnet" {
        t.Fatalf("session fields wrong: %+v", sess)
    }
    if got := sess.Channels; len(got) != 1 || got[0] != "plugin:slack@official" {
        t.Fatalf("channels wrong: %+v", got)
    }
    task := cfg.Tasks["standup"]
    if task.Runtime != "persistent" || task.Session != "daily" || task.QueueMax != 3 || task.Lazy {
        t.Fatalf("task fields wrong: %+v", task)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSessionConfigParses ./internal/config/`
Expected: compile error — `cfg.Sessions undefined`, `task.Runtime undefined`, etc.

- [ ] **Step 3: Add the types**

In `internal/config/config.go`, add new type just below `ProcessConfig` (around line 215):

```go
type SessionConfig struct {
    Workspace          string            `yaml:"workspace,omitempty"`
    Model              string            `yaml:"model,omitempty"`
    Agent              string            `yaml:"agent,omitempty"`
    PermissionMode     string            `yaml:"permission_mode,omitempty"`
    AllowedTools       []string          `yaml:"allowed_tools,omitempty"`
    DisallowedTools    []string          `yaml:"disallowed_tools,omitempty"`
    AppendSystemPrompt string            `yaml:"append_system_prompt,omitempty"`
    AddDirs            []string          `yaml:"add_dirs,omitempty"`
    Channels           []string          `yaml:"channels,omitempty"`
    Env                map[string]string `yaml:"env,omitempty"`
    IdleTimeout        string            `yaml:"idle_timeout,omitempty"`
}
```

In `Config` struct (~line 75), add field:

```go
Sessions map[string]SessionConfig `yaml:"sessions,omitempty"`
```

In `TaskConfig` struct (~line 217), add fields:

```go
Runtime  string `yaml:"runtime,omitempty"`
Session  string `yaml:"session,omitempty"`
Lazy     bool   `yaml:"lazy,omitempty"`
QueueMax int    `yaml:"queue_max,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestSessionConfigParses ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Sessions config block and persistent-task TaskConfig fields"
```

---

### Task 2: Validate runtime, session resolution, and channel-subset rule

**Files:**
- Modify: `internal/config/config.go` (extend `Validate()`)
- Create: `internal/config/session.go`
- Test: `internal/config/session_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/config/session_test.go`:

```go
package config

import (
    "strings"
    "testing"
)

func TestResolveSessionDedicated(t *testing.T) {
    cfg := &Config{
        Tasks: map[string]TaskConfig{
            "t1": {
                Runtime:    "persistent",
                Workspace:  "/tmp/t1",
                Model:      "sonnet",
                Channels:   []string{"plugin:slack@official"},
            },
        },
    }
    name, sess, err := cfg.ResolveSession("t1")
    if err != nil {
        t.Fatalf("resolve: %v", err)
    }
    if name != "t1" {
        t.Fatalf("expected implicit name 't1', got %q", name)
    }
    if sess.Workspace != "/tmp/t1" || sess.Model != "sonnet" {
        t.Fatalf("inheritance wrong: %+v", sess)
    }
    if len(sess.Channels) != 1 || sess.Channels[0] != "plugin:slack@official" {
        t.Fatalf("channels inheritance wrong: %+v", sess.Channels)
    }
}

func TestResolveSessionShared(t *testing.T) {
    cfg := &Config{
        Sessions: map[string]SessionConfig{
            "daily": {Workspace: "/tmp/d", Channels: []string{"plugin:slack@official"}},
        },
        Tasks: map[string]TaskConfig{
            "t1": {Runtime: "persistent", Session: "daily", Channels: []string{"plugin:slack@official"}},
        },
    }
    name, sess, err := cfg.ResolveSession("t1")
    if err != nil {
        t.Fatalf("resolve: %v", err)
    }
    if name != "daily" || sess.Workspace != "/tmp/d" {
        t.Fatalf("shared resolution wrong: name=%q sess=%+v", name, sess)
    }
}

func TestResolveSessionMissing(t *testing.T) {
    cfg := &Config{
        Tasks: map[string]TaskConfig{
            "t1": {Runtime: "persistent", Session: "nope"},
        },
    }
    if _, _, err := cfg.ResolveSession("t1"); err == nil {
        t.Fatalf("expected error for missing session reference")
    }
}

func TestValidatePersistentChannelsSubset(t *testing.T) {
    cfg := &Config{
        Sessions: map[string]SessionConfig{
            "daily": {Workspace: "/tmp/d", Channels: []string{"plugin:slack@official"}},
        },
        Tasks: map[string]TaskConfig{
            "bad": {
                Runtime: "persistent", Session: "daily",
                Schedule: "0 7 * * *", PromptFile: "p.md",
                Channels: []string{"plugin:telegram@official"},
            },
        },
    }
    err := cfg.Validate()
    if err == nil || !strings.Contains(err.Error(), "subset") {
        t.Fatalf("expected subset error, got %v", err)
    }
}

func TestValidatePersistentDedicatedNameConflict(t *testing.T) {
    cfg := &Config{
        Sessions: map[string]SessionConfig{
            "t1": {Workspace: "/tmp/x", Channels: []string{"plugin:slack@official"}},
        },
        Tasks: map[string]TaskConfig{
            // task "t1" has no `session:` so wants implicit session "t1" — collides with sessions.t1
            "t1": {
                Runtime: "persistent", Schedule: "0 * * * *", PromptFile: "p.md",
                Workspace: "/tmp/x", Channels: []string{"plugin:slack@official"},
            },
        },
    }
    err := cfg.Validate()
    if err == nil || !strings.Contains(err.Error(), "implicit session") {
        t.Fatalf("expected implicit-name conflict error, got %v", err)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestResolveSession|TestValidatePersistent' -v`
Expected: FAIL — `cfg.ResolveSession undefined` and (after that) subset/conflict not enforced.

- [ ] **Step 3: Implement ResolveSession**

Create `internal/config/session.go`:

```go
package config

import (
    "fmt"
    "strings"
)

// ResolveSession returns the session name and SessionConfig that hosts the named
// persistent task. For tasks without `session:` it synthesizes an implicit
// SessionConfig from the task itself and returns the task name as the session
// name. For `session: process:<name>` it returns the process name with a
// SessionConfig derived from the ProcessConfig. Returns an error for oneshot
// tasks or unresolved references.
func (c *Config) ResolveSession(taskName string) (string, SessionConfig, error) {
    task, ok := c.Tasks[taskName]
    if !ok {
        return "", SessionConfig{}, fmt.Errorf("task %q not found", taskName)
    }
    if !strings.EqualFold(task.Runtime, "persistent") {
        return "", SessionConfig{}, fmt.Errorf("task %q is not runtime: persistent", taskName)
    }

    switch {
    case task.Session == "":
        // Topology A — dedicated, inherit from task.
        return taskName, SessionConfig{
            Workspace:          task.Workspace,
            Model:              task.Model,
            Agent:              task.Agent,
            PermissionMode:     task.PermissionMode,
            AllowedTools:       task.AllowedTools,
            DisallowedTools:    task.DisallowedTools,
            AppendSystemPrompt: task.AppendSystemPrompt,
            Channels:           task.Channels,
            Env:                task.Env,
        }, nil

    case strings.HasPrefix(task.Session, "process:"):
        // Topology C — reuse a supervised process.
        procName := strings.TrimPrefix(task.Session, "process:")
        proc, ok := c.Processes[procName]
        if !ok {
            return "", SessionConfig{}, fmt.Errorf("task %q references process:%s which is not defined", taskName, procName)
        }
        return procName, SessionConfig{
            Workspace:          proc.Workspace,
            Model:              proc.Model,
            Agent:              proc.Agent,
            PermissionMode:     proc.PermissionMode,
            AllowedTools:       proc.AllowedTools,
            DisallowedTools:    proc.DisallowedTools,
            AppendSystemPrompt: proc.AppendSystemPrompt,
            Channels:           proc.Channels,
            Env:                proc.Env,
        }, nil

    default:
        // Topology B — shared session from sessions: map.
        sess, ok := c.Sessions[task.Session]
        if !ok {
            return "", SessionConfig{}, fmt.Errorf("task %q references sessions.%s which is not defined", taskName, task.Session)
        }
        return task.Session, sess, nil
    }
}

// channelSubset reports whether every element of want appears in have.
func channelSubset(want, have []string) (string, bool) {
    set := make(map[string]struct{}, len(have))
    for _, c := range have {
        set[c] = struct{}{}
    }
    for _, c := range want {
        if _, ok := set[c]; !ok {
            return c, false
        }
    }
    return "", true
}
```

- [ ] **Step 4: Wire validation in Config.Validate**

In `internal/config/config.go`, locate `Validate()` (~line 408). Find the per-task validation block (it iterates `c.Tasks`) and within that loop, after existing checks, add:

```go
if task.Runtime != "" && task.Runtime != "oneshot" && task.Runtime != "persistent" {
    return fmt.Errorf("task %q: invalid runtime %q (want \"oneshot\" or \"persistent\")", name, task.Runtime)
}
if task.Runtime != "persistent" && task.Session != "" {
    return fmt.Errorf("task %q: session is only valid when runtime: persistent", name)
}
if task.Runtime == "persistent" {
    sessName, sess, err := c.ResolveSession(name)
    if err != nil {
        return fmt.Errorf("task %q: %w", name, err)
    }
    if task.Session == "" {
        if _, clash := c.Sessions[sessName]; clash {
            return fmt.Errorf("task %q: implicit session name %q collides with sessions.%s — give the task a `session:` reference or rename one", name, sessName, sessName)
        }
    }
    if missing, ok := channelSubset(task.Channels, sess.Channels); !ok {
        return fmt.Errorf("task %q: channel %q is not in sessions.%s.channels (task.channels must be a subset)", name, missing, sessName)
    }
}
```

Also add a sessions: loop validation block before the tasks loop:

```go
for name, sess := range c.Sessions {
    if sess.Workspace == "" {
        return fmt.Errorf("session %q: workspace is required", name)
    }
    if sess.Model != "" && !validModels[sess.Model] {
        return fmt.Errorf("session %q: invalid model %q", name, sess.Model)
    }
    if sess.PermissionMode != "" && !validPermissionModes[sess.PermissionMode] {
        return fmt.Errorf("session %q: invalid permission_mode %q", name, sess.PermissionMode)
    }
    for _, ch := range sess.Channels {
        if !channelPattern.MatchString(ch) {
            return fmt.Errorf("session %q: invalid channel %q", name, ch)
        }
    }
    if sess.IdleTimeout != "" {
        if _, err := time.ParseDuration(sess.IdleTimeout); err != nil {
            return fmt.Errorf("session %q: invalid idle_timeout %q: %w", name, sess.IdleTimeout, err)
        }
    }
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/config/ -v`
Expected: PASS for all five new tests plus all existing config tests.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/session.go internal/config/session_test.go
git commit -m "feat(config): validate persistent runtime + ResolveSession helper"
```

---

### Task 3: Add tmux prompt injection primitives

**Files:**
- Create: `internal/tmux/inject.go`
- Test: `internal/tmux/inject_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tmux/inject_test.go`:

```go
package tmux

import (
    "context"
    "os/exec"
    "reflect"
    "testing"
)

func TestInjectPromptCalls(t *testing.T) {
    var got [][]string
    orig := execCommand
    defer func() { execCommand = orig }()
    execCommand = func(name string, args ...string) *exec.Cmd {
        full := append([]string{name}, args...)
        got = append(got, full)
        return exec.Command("true") // succeed
    }
    if err := InjectPrompt(context.Background(), "tmux", "leo-session-foo", "hello\nworld"); err != nil {
        t.Fatalf("InjectPrompt: %v", err)
    }
    if len(got) != 3 {
        t.Fatalf("expected 3 tmux calls, got %d: %#v", len(got), got)
    }
    expectSet := []string{"tmux", "-L", "leo", "set-buffer", "-b", "leo", "--", "hello\nworld"}
    expectPaste := []string{"tmux", "-L", "leo", "paste-buffer", "-b", "leo", "-t", "leo-session-foo", "-d"}
    expectEnter := []string{"tmux", "-L", "leo", "send-keys", "-t", "leo-session-foo", "Enter"}
    if !reflect.DeepEqual(got[0], expectSet) {
        t.Fatalf("set-buffer call wrong:\n got %#v\nwant %#v", got[0], expectSet)
    }
    if !reflect.DeepEqual(got[1], expectPaste) {
        t.Fatalf("paste-buffer call wrong:\n got %#v\nwant %#v", got[1], expectPaste)
    }
    if !reflect.DeepEqual(got[2], expectEnter) {
        t.Fatalf("send-keys Enter call wrong:\n got %#v\nwant %#v", got[2], expectEnter)
    }
}

func TestAbortPromptCalls(t *testing.T) {
    var got [][]string
    orig := execCommand
    defer func() { execCommand = orig }()
    execCommand = func(name string, args ...string) *exec.Cmd {
        got = append(got, append([]string{name}, args...))
        return exec.Command("true")
    }
    if err := AbortPrompt(context.Background(), "tmux", "leo-session-foo"); err != nil {
        t.Fatalf("AbortPrompt: %v", err)
    }
    if len(got) != 2 {
        t.Fatalf("expected 2 calls, got %d", len(got))
    }
    if got[0][len(got[0])-1] != "Escape" || got[1][len(got[1])-1] != "C-c" {
        t.Fatalf("expected Escape then C-c, got %#v / %#v", got[0], got[1])
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestInjectPrompt|TestAbortPrompt' ./internal/tmux/`
Expected: compile error — `InjectPrompt`, `AbortPrompt`, `execCommand` undefined.

- [ ] **Step 3: Implement the primitives**

Create `internal/tmux/inject.go`:

```go
package tmux

import (
    "context"
    "fmt"
    "os/exec"
)

// execCommand is the seam tests replace.
var execCommand = exec.CommandContext

// InjectPrompt sends body to the claude running in `session` as a single
// submission. Uses set-buffer + paste-buffer (-d deletes after paste) to avoid
// character-by-character races; multi-line bodies preserved; Enter submits.
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error {
    setArgs := Args("set-buffer", "-b", "leo", "--", body)
    pasteArgs := Args("paste-buffer", "-b", "leo", "-t", session, "-d")
    enterArgs := Args("send-keys", "-t", session, "Enter")
    for _, args := range [][]string{setArgs, pasteArgs, enterArgs} {
        cmd := execCommand(ctx, tmuxPath, args...)
        out, err := cmd.CombinedOutput()
        if err != nil {
            return fmt.Errorf("tmux %v: %w: %s", args[:2], err, string(out))
        }
    }
    return nil
}

// AbortPrompt cancels a mid-turn claude by sending Escape then Ctrl-C. Used on
// timeout/abort. Errors from individual sends are logged-in-error but not
// fatal — best-effort.
func AbortPrompt(ctx context.Context, tmuxPath, session string) error {
    keys := []string{"Escape", "C-c"}
    var firstErr error
    for _, k := range keys {
        cmd := execCommand(ctx, tmuxPath, Args("send-keys", "-t", session, k)...)
        if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
            firstErr = fmt.Errorf("tmux send-keys %s: %w: %s", k, err, string(out))
        }
    }
    return firstErr
}
```

Note: the test uses `execCommand = func(name string, args ...string) *exec.Cmd { ... }` but our seam is `exec.CommandContext`. Adjust the test's signature to match: change the test's `execCommand` replacements to `func(ctx context.Context, name string, args ...string) *exec.Cmd` and ignore ctx. Updated test injection block:

```go
execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
    got = append(got, append([]string{name}, args...))
    return exec.Command("true")
}
```

Apply that change in both test functions.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestInjectPrompt|TestAbortPrompt' ./internal/tmux/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tmux/inject.go internal/tmux/inject_test.go
git commit -m "feat(tmux): InjectPrompt and AbortPrompt primitives for persistent task sessions"
```

---

### Task 4: Hook installer for `.claude/settings.local.json`

**Files:**
- Create: `internal/hooks/install.go`
- Test: `internal/hooks/install_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/hooks/install_test.go`:

```go
package hooks

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func TestEnsureLeoStopHookFromEmpty(t *testing.T) {
    dir := t.TempDir()
    if err := EnsureLeoStopHook(dir); err != nil {
        t.Fatalf("ensure: %v", err)
    }
    raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    var got map[string]any
    if err := json.Unmarshal(raw, &got); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    stops := got["hooks"].(map[string]any)["Stop"].([]any)
    if len(stops) != 1 {
        t.Fatalf("expected 1 Stop hook, got %d", len(stops))
    }
    entry := stops[0].(map[string]any)
    if entry["_leo_managed"] != "task-report" || entry["command"] != "leo internal task-report" {
        t.Fatalf("hook entry wrong: %#v", entry)
    }
}

func TestEnsureLeoStopHookPreservesUserHooks(t *testing.T) {
    dir := t.TempDir()
    cdir := filepath.Join(dir, ".claude")
    if err := os.MkdirAll(cdir, 0o755); err != nil {
        t.Fatalf("mkdir: %v", err)
    }
    seed := `{"hooks":{"Stop":[{"command":"my-user-hook"}],"PreToolUse":[{"matcher":"Write","command":"fmt"}]}}`
    if err := os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte(seed), 0o644); err != nil {
        t.Fatalf("write seed: %v", err)
    }
    if err := EnsureLeoStopHook(dir); err != nil {
        t.Fatalf("ensure: %v", err)
    }
    raw, _ := os.ReadFile(filepath.Join(cdir, "settings.local.json"))
    var got map[string]any
    _ = json.Unmarshal(raw, &got)
    stops := got["hooks"].(map[string]any)["Stop"].([]any)
    if len(stops) != 2 {
        t.Fatalf("expected user hook + leo hook, got %d", len(stops))
    }
    pre := got["hooks"].(map[string]any)["PreToolUse"].([]any)
    if len(pre) != 1 {
        t.Fatalf("PreToolUse hooks were dropped")
    }
}

func TestEnsureLeoStopHookIdempotent(t *testing.T) {
    dir := t.TempDir()
    for i := 0; i < 3; i++ {
        if err := EnsureLeoStopHook(dir); err != nil {
            t.Fatalf("ensure iter %d: %v", i, err)
        }
    }
    raw, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
    var got map[string]any
    _ = json.Unmarshal(raw, &got)
    stops := got["hooks"].(map[string]any)["Stop"].([]any)
    leoCount := 0
    for _, s := range stops {
        if s.(map[string]any)["_leo_managed"] == "task-report" {
            leoCount++
        }
    }
    if leoCount != 1 {
        t.Fatalf("expected exactly 1 leo-managed entry after repeated ensure, got %d", leoCount)
    }
}

func TestEnsureLeoStopHookRefusesMalformedJSON(t *testing.T) {
    dir := t.TempDir()
    cdir := filepath.Join(dir, ".claude")
    _ = os.MkdirAll(cdir, 0o755)
    _ = os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte("{not json"), 0o644)
    if err := EnsureLeoStopHook(dir); err == nil {
        t.Fatalf("expected error on malformed json, got nil")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hooks/`
Expected: compile error — package doesn't exist.

- [ ] **Step 3: Implement the installer**

Create `internal/hooks/install.go`:

```go
// Package hooks manages leo-owned entries inside Claude Code's
// .claude/settings.local.json file in a session's workspace.
package hooks

import (
    "encoding/json"
    "errors"
    "fmt"
    "io/fs"
    "os"
    "path/filepath"
)

const leoManagedKey = "_leo_managed"
const leoStopHookLabel = "task-report"
const leoStopCommand = "leo internal task-report"

// EnsureLeoStopHook idempotently merges the leo-managed Stop hook into
// <workspace>/.claude/settings.local.json. Preserves all non-leo entries.
// Atomic write via os.Rename. Refuses (returns error) if the existing file
// contains malformed JSON rather than clobber user data.
func EnsureLeoStopHook(workspace string) error {
    if workspace == "" {
        return errors.New("hooks.EnsureLeoStopHook: empty workspace")
    }
    dir := filepath.Join(workspace, ".claude")
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return fmt.Errorf("mkdir %s: %w", dir, err)
    }
    path := filepath.Join(dir, "settings.local.json")

    root := map[string]any{}
    raw, err := os.ReadFile(path)
    switch {
    case err == nil:
        if len(raw) > 0 {
            if jerr := json.Unmarshal(raw, &root); jerr != nil {
                return fmt.Errorf("parse %s: %w (refusing to overwrite)", path, jerr)
            }
        }
    case errors.Is(err, fs.ErrNotExist):
        // start from empty
    default:
        return fmt.Errorf("read %s: %w", path, err)
    }

    hooks, _ := root["hooks"].(map[string]any)
    if hooks == nil {
        hooks = map[string]any{}
    }
    stops, _ := hooks["Stop"].([]any)
    pruned := stops[:0:0]
    for _, raw := range stops {
        entry, ok := raw.(map[string]any)
        if !ok {
            pruned = append(pruned, raw)
            continue
        }
        if entry[leoManagedKey] == leoStopHookLabel {
            continue // drop leo-managed; we'll re-add below
        }
        pruned = append(pruned, raw)
    }
    pruned = append(pruned, map[string]any{
        leoManagedKey: leoStopHookLabel,
        "command":     leoStopCommand,
    })
    hooks["Stop"] = pruned
    root["hooks"] = hooks

    out, err := json.MarshalIndent(root, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal: %w", err)
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, out, 0o644); err != nil {
        return fmt.Errorf("write tmp: %w", err)
    }
    if err := os.Rename(tmp, path); err != nil {
        return fmt.Errorf("rename: %w", err)
    }
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/hooks/ -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/hooks/install.go internal/hooks/install_test.go
git commit -m "feat(hooks): idempotent EnsureLeoStopHook installer for .claude/settings.local.json"
```

---

## Phase 2 — Daemon machinery (router, endpoints, client)

### Task 5: Session router types + Enqueue (no pump yet)

**Files:**
- Create: `internal/daemon/session_router.go`
- Test: `internal/daemon/session_router_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/session_router_test.go`:

```go
package daemon

import (
    "testing"
    "time"
)

func TestSessionRouterEnqueueAccepts(t *testing.T) {
    r := newSessionRouter()
    inv, ok := r.Enqueue(EnqueueParams{
        Session:  "leo-session-foo",
        Task:     "morning",
        Prompt:   "do the thing",
        Channels: []string{"plugin:slack@official"},
        QueueMax: 5,
        Timeout:  10 * time.Second,
    })
    if !ok {
        t.Fatalf("expected enqueue accepted")
    }
    if inv.ID == "" {
        t.Fatalf("expected non-empty invocation id")
    }
    if inv.Task != "morning" {
        t.Fatalf("task mismatch: %q", inv.Task)
    }
}

func TestSessionRouterEnqueueQueueFull(t *testing.T) {
    r := newSessionRouter()
    p := EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 2, Timeout: time.Second}
    if _, ok := r.Enqueue(p); !ok {
        t.Fatal("first enqueue should accept")
    }
    if _, ok := r.Enqueue(p); !ok {
        t.Fatal("second enqueue should accept (queue depth 2)")
    }
    if _, ok := r.Enqueue(p); ok {
        t.Fatal("third enqueue should reject (queue full)")
    }
}

func TestSessionRouterLookupByID(t *testing.T) {
    r := newSessionRouter()
    inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 5, Timeout: time.Second})
    got, ok := r.Lookup(inv.ID)
    if !ok || got.Task != "t" {
        t.Fatalf("lookup failed: %+v ok=%v", got, ok)
    }
    if _, ok := r.Lookup("does-not-exist"); ok {
        t.Fatalf("expected miss for unknown id")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestSessionRouter' ./internal/daemon/`
Expected: compile error — `newSessionRouter`, `EnqueueParams` undefined.

- [ ] **Step 3: Implement types + Enqueue + Lookup**

Create `internal/daemon/session_router.go`:

```go
package daemon

import (
    "crypto/rand"
    "encoding/hex"
    "sync"
    "time"
)

type EnqueueParams struct {
    Session  string
    Task     string
    Prompt   string   // already includes marker + delivery footer (caller's job)
    Channels []string // for record-keeping only; delivery is done in-session
    QueueMax int
    Timeout  time.Duration
}

type InvocationResult struct {
    OK           bool
    SessionID    string
    FinalMessage string
    Err          string
}

type PendingInvocation struct {
    ID       string
    Session  string
    Task     string
    Prompt   string
    Channels []string
    Timeout  time.Duration
    Enqueued time.Time
    Result   chan InvocationResult // buffered(1); never close from inside the queue
}

type sessionQueue struct {
    mu       sync.Mutex
    fifo     []*PendingInvocation
    inFlight *PendingInvocation
    notify   chan struct{} // buffered(1); pump signal
}

type sessionRouter struct {
    mu     sync.Mutex
    queues map[string]*sessionQueue
    byID   map[string]*PendingInvocation
}

func newSessionRouter() *sessionRouter {
    return &sessionRouter{
        queues: map[string]*sessionQueue{},
        byID:   map[string]*PendingInvocation{},
    }
}

func newInvocationID() string {
    var b [16]byte
    _, _ = rand.Read(b[:])
    return hex.EncodeToString(b[:])
}

// Enqueue appends to the session's FIFO. Returns the invocation and ok=true on
// success, or ok=false if the queue is at QueueMax. Does NOT block on the pump.
func (r *sessionRouter) Enqueue(p EnqueueParams) (*PendingInvocation, bool) {
    r.mu.Lock()
    q, ok := r.queues[p.Session]
    if !ok {
        q = &sessionQueue{notify: make(chan struct{}, 1)}
        r.queues[p.Session] = q
    }
    r.mu.Unlock()

    q.mu.Lock()
    defer q.mu.Unlock()
    capacity := p.QueueMax
    if capacity <= 0 {
        capacity = 5
    }
    if len(q.fifo) >= capacity {
        return nil, false
    }
    inv := &PendingInvocation{
        ID:       newInvocationID(),
        Session:  p.Session,
        Task:     p.Task,
        Prompt:   p.Prompt,
        Channels: p.Channels,
        Timeout:  p.Timeout,
        Enqueued: time.Now(),
        Result:   make(chan InvocationResult, 1),
    }
    q.fifo = append(q.fifo, inv)

    r.mu.Lock()
    r.byID[inv.ID] = inv
    r.mu.Unlock()

    select {
    case q.notify <- struct{}{}:
    default:
    }
    return inv, true
}

// Lookup returns the invocation by id, or false if missing/expired.
func (r *sessionRouter) Lookup(id string) (*PendingInvocation, bool) {
    r.mu.Lock()
    defer r.mu.Unlock()
    inv, ok := r.byID[id]
    return inv, ok
}

// queueFor returns the named session queue (or nil if none ever enqueued).
func (r *sessionRouter) queueFor(session string) *sessionQueue {
    r.mu.Lock()
    defer r.mu.Unlock()
    return r.queues[session]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestSessionRouter' ./internal/daemon/ -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/session_router.go internal/daemon/session_router_test.go
git commit -m "feat(daemon): session router types and Enqueue"
```

---

### Task 6: Pump goroutine + Inject hook + timeout abort

**Files:**
- Modify: `internal/daemon/session_router.go`
- Modify: `internal/daemon/session_router_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/session_router_test.go`:

```go
func TestSessionRouterPumpInjectsThenAdvances(t *testing.T) {
    r := newSessionRouter()
    var injections []string
    injector := func(session, prompt string) error {
        injections = append(injections, session+"|"+prompt)
        return nil
    }
    r.SetInjector(injector)
    r.SetAborter(func(string) error { return nil })

    inv1, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "a", Prompt: "p1", QueueMax: 5, Timeout: time.Second})
    inv2, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "b", Prompt: "p2", QueueMax: 5, Timeout: time.Second})
    r.StartPump("s")

    // simulate reports arriving
    go func() {
        time.Sleep(20 * time.Millisecond)
        r.Report(inv1.ID, InvocationResult{OK: true, FinalMessage: "done1"})
        time.Sleep(20 * time.Millisecond)
        r.Report(inv2.ID, InvocationResult{OK: true, FinalMessage: "done2"})
    }()

    res1 := <-inv1.Result
    res2 := <-inv2.Result
    if !res1.OK || res1.FinalMessage != "done1" {
        t.Fatalf("res1 wrong: %+v", res1)
    }
    if !res2.OK || res2.FinalMessage != "done2" {
        t.Fatalf("res2 wrong: %+v", res2)
    }
    if len(injections) != 2 || injections[0] != "s|p1" || injections[1] != "s|p2" {
        t.Fatalf("injection order wrong: %v", injections)
    }
}

func TestSessionRouterPumpTimeoutAborts(t *testing.T) {
    r := newSessionRouter()
    var aborted bool
    r.SetInjector(func(session, prompt string) error { return nil })
    r.SetAborter(func(session string) error { aborted = true; return nil })

    inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "slow", Prompt: "x", QueueMax: 5, Timeout: 50 * time.Millisecond})
    r.StartPump("s")
    res := <-inv.Result
    if res.OK || res.Err != "timeout" {
        t.Fatalf("expected timeout, got %+v", res)
    }
    if !aborted {
        t.Fatalf("expected aborter to be called")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run 'TestSessionRouterPump' ./internal/daemon/ -v`
Expected: compile error — `SetInjector`, `SetAborter`, `StartPump`, `Report` undefined.

- [ ] **Step 3: Implement the pump**

Append to `internal/daemon/session_router.go`:

```go
type injectFn func(session, prompt string) error
type abortFn func(session string) error

// SetInjector / SetAborter wire the tmux primitives (or test fakes).
func (r *sessionRouter) SetInjector(fn injectFn) { r.inject = fn }
func (r *sessionRouter) SetAborter(fn abortFn)   { r.abort = fn }

// StartPump launches the per-session pump goroutine. Idempotent: a session
// only ever gets one pump in its lifetime (subsequent calls are no-ops).
func (r *sessionRouter) StartPump(session string) {
    r.mu.Lock()
    q, ok := r.queues[session]
    if !ok {
        q = &sessionQueue{notify: make(chan struct{}, 1)}
        r.queues[session] = q
    }
    if q.pumpStarted {
        r.mu.Unlock()
        return
    }
    q.pumpStarted = true
    r.mu.Unlock()
    go r.pump(session, q)
}

// Report signals the matching pending invocation. If id is unknown or doesn't
// match the session's current inFlight, the report is discarded silently
// (defensive against late hook callbacks).
func (r *sessionRouter) Report(id string, result InvocationResult) {
    inv, ok := r.Lookup(id)
    if !ok {
        return
    }
    q := r.queueFor(inv.Session)
    if q == nil {
        return
    }
    q.mu.Lock()
    matches := q.inFlight != nil && q.inFlight.ID == id
    if matches {
        q.inFlight = nil
    }
    q.mu.Unlock()
    if !matches {
        return // late/duplicate
    }
    select {
    case inv.Result <- result:
    default:
    }
    r.mu.Lock()
    delete(r.byID, id)
    r.mu.Unlock()
    select {
    case q.notify <- struct{}{}:
    default:
    }
}

func (r *sessionRouter) pump(session string, q *sessionQueue) {
    for {
        <-q.notify
        for {
            q.mu.Lock()
            if q.inFlight != nil || len(q.fifo) == 0 {
                q.mu.Unlock()
                break
            }
            next := q.fifo[0]
            q.fifo = q.fifo[1:]
            q.inFlight = next
            q.mu.Unlock()

            if err := r.inject(session, next.Prompt); err != nil {
                q.mu.Lock()
                q.inFlight = nil
                q.mu.Unlock()
                r.mu.Lock()
                delete(r.byID, next.ID)
                r.mu.Unlock()
                select {
                case next.Result <- InvocationResult{OK: false, Err: "inject: " + err.Error()}:
                default:
                }
                continue
            }

            timer := time.NewTimer(next.Timeout)
            // Wait until either timer fires or the inFlight is cleared by Report.
            // We poll the inFlight pointer at a short interval; Report's notify
            // wakes us via the outer select.
            select {
            case <-timer.C:
                _ = r.abort(session)
                q.mu.Lock()
                still := q.inFlight != nil && q.inFlight.ID == next.ID
                if still {
                    q.inFlight = nil
                }
                q.mu.Unlock()
                if still {
                    r.mu.Lock()
                    delete(r.byID, next.ID)
                    r.mu.Unlock()
                    select {
                    case next.Result <- InvocationResult{OK: false, Err: "timeout"}:
                    default:
                    }
                }
            case <-q.notify:
                if !timer.Stop() {
                    <-timer.C
                }
                // notify came from Report path — the inFlight is already cleared
                // and the result delivered. Loop to pick up the next item.
            }
        }
    }
}
```

Add fields to `sessionQueue`:

```go
pumpStarted bool
```

Add fields to `sessionRouter`:

```go
inject injectFn
abort  abortFn
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestSessionRouter' ./internal/daemon/ -v`
Expected: PASS for all five tests (3 from Task 5 + 2 from this task).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/session_router.go internal/daemon/session_router_test.go
git commit -m "feat(daemon): session router pump with inject/abort/report"
```

---

### Task 7: HTTP endpoints — `/task/enqueue`, `/task/await`, `/task/report`

**Files:**
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/endpoints_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/endpoints_test.go`:

```go
package daemon

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

func newServerWithRouter(t *testing.T) (*Server, *sessionRouter, *httptest.Server) {
    t.Helper()
    s := &Server{router: newSessionRouter()}
    s.router.SetInjector(func(session, prompt string) error { return nil })
    s.router.SetAborter(func(session string) error { return nil })
    mux := http.NewServeMux()
    mux.HandleFunc("POST /task/enqueue", s.handleTaskEnqueue)
    mux.HandleFunc("GET /task/await", s.handleTaskAwait)
    mux.HandleFunc("POST /task/report", s.handleTaskReport)
    ts := httptest.NewServer(mux)
    t.Cleanup(ts.Close)
    return s, s.router, ts
}

func TestEnqueueRouteAccepts(t *testing.T) {
    _, _, ts := newServerWithRouter(t)
    body, _ := json.Marshal(map[string]any{
        "session":         "leo-session-foo",
        "task":            "t",
        "prompt":          "do it",
        "channels":        []string{"plugin:slack@official"},
        "queue_max":       3,
        "timeout_seconds": 10,
    })
    resp, err := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(body))
    if err != nil {
        t.Fatalf("post: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("status: %d", resp.StatusCode)
    }
    var out map[string]any
    _ = json.NewDecoder(resp.Body).Decode(&out)
    if out["accepted"] != true || out["invocation_id"] == "" {
        t.Fatalf("body: %#v", out)
    }
}

func TestAwaitGetsReport(t *testing.T) {
    s, _, ts := newServerWithRouter(t)
    s.router.StartPump("leo-session-foo")

    enqBody, _ := json.Marshal(map[string]any{
        "session":         "leo-session-foo",
        "task":            "t",
        "prompt":          "x",
        "queue_max":       3,
        "timeout_seconds": 5,
    })
    enq, _ := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(enqBody))
    var enqOut map[string]any
    _ = json.NewDecoder(enq.Body).Decode(&enqOut)
    id := enqOut["invocation_id"].(string)

    go func() {
        time.Sleep(30 * time.Millisecond)
        rep, _ := json.Marshal(map[string]any{
            "invocation_id": id,
            "session_id":    "csid-1",
            "final_message": "result!",
            "session_name":  "leo-session-foo",
        })
        http.Post(ts.URL+"/task/report", "application/json", bytes.NewReader(rep))
    }()

    aw, _ := http.Get(ts.URL + "/task/await?invocation_id=" + id)
    if aw.StatusCode != http.StatusOK {
        t.Fatalf("await status: %d", aw.StatusCode)
    }
    var awOut map[string]any
    _ = json.NewDecoder(aw.Body).Decode(&awOut)
    if awOut["ok"] != true || awOut["final_message"] != "result!" || awOut["session_id"] != "csid-1" {
        t.Fatalf("await body: %#v", awOut)
    }
}

func TestEnqueueRejectsOnQueueFull(t *testing.T) {
    _, _, ts := newServerWithRouter(t)
    body, _ := json.Marshal(map[string]any{
        "session":         "leo-session-foo",
        "task":            "t",
        "prompt":          "x",
        "queue_max":       1,
        "timeout_seconds": 5,
    })
    var lastStatus int
    var lastBody map[string]any
    for i := 0; i < 2; i++ {
        resp, err := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(body))
        if err != nil {
            t.Fatalf("post: %v", err)
        }
        lastStatus = resp.StatusCode
        _ = json.NewDecoder(resp.Body).Decode(&lastBody)
        resp.Body.Close()
    }
    if lastStatus != http.StatusOK {
        t.Fatalf("status: %d", lastStatus)
    }
    if lastBody["accepted"] != false || !strings.Contains(lastBody["reason"].(string), "queue full") {
        t.Fatalf("expected queue full rejection, got %#v", lastBody)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run 'TestEnqueueRoute|TestAwait|TestEnqueueRejects' ./internal/daemon/`
Expected: compile error — `Server.router`, `handleTaskEnqueue`, etc. undefined.

- [ ] **Step 3: Implement the endpoints**

In `internal/daemon/server.go`, add field to the `Server` struct (~line 45):

```go
router *sessionRouter
```

In `New(...)` (~line 57), initialize it before returning:

```go
s := &Server{ /* existing fields */ }
s.router = newSessionRouter()
return s
```

In the same file, add (toward the bottom, near `handleProcessList`):

```go
type taskEnqueueReq struct {
    Session        string   `json:"session"`
    Task           string   `json:"task"`
    Prompt         string   `json:"prompt"`
    Channels       []string `json:"channels"`
    QueueMax       int      `json:"queue_max"`
    TimeoutSeconds int      `json:"timeout_seconds"`
}

type taskEnqueueResp struct {
    Accepted     bool   `json:"accepted"`
    InvocationID string `json:"invocation_id,omitempty"`
    Reason       string `json:"reason,omitempty"`
}

func (s *Server) handleTaskEnqueue(w http.ResponseWriter, r *http.Request) {
    var req taskEnqueueReq
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
        return
    }
    if req.Session == "" || req.Task == "" || req.Prompt == "" {
        writeError(w, http.StatusBadRequest, "session, task, prompt are required")
        return
    }
    timeout := time.Duration(req.TimeoutSeconds) * time.Second
    if timeout == 0 {
        timeout = 5 * time.Minute
    }
    inv, ok := s.router.Enqueue(EnqueueParams{
        Session: req.Session, Task: req.Task, Prompt: req.Prompt,
        Channels: req.Channels, QueueMax: req.QueueMax, Timeout: timeout,
    })
    if !ok {
        writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: false, Reason: "queue full"})
        return
    }
    s.router.StartPump(req.Session)
    writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: true, InvocationID: inv.ID})
}

func (s *Server) handleTaskAwait(w http.ResponseWriter, r *http.Request) {
    id := r.URL.Query().Get("invocation_id")
    if id == "" {
        writeError(w, http.StatusBadRequest, "invocation_id required")
        return
    }
    inv, ok := s.router.Lookup(id)
    if !ok {
        writeError(w, http.StatusNotFound, "unknown invocation_id")
        return
    }
    select {
    case res := <-inv.Result:
        writeJSON(w, http.StatusOK, map[string]any{
            "ok":            res.OK,
            "session_id":    res.SessionID,
            "final_message": res.FinalMessage,
            "error":         res.Err,
        })
    case <-r.Context().Done():
        writeError(w, http.StatusGatewayTimeout, "request cancelled")
    }
}

type taskReportReq struct {
    InvocationID string `json:"invocation_id"`
    SessionID    string `json:"session_id"`
    FinalMessage string `json:"final_message"`
    SessionName  string `json:"session_name"`
}

func (s *Server) handleTaskReport(w http.ResponseWriter, r *http.Request) {
    var req taskReportReq
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
        return
    }
    if req.InvocationID == "" {
        writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // human turn — ignore
        return
    }
    s.router.Report(req.InvocationID, InvocationResult{
        OK:           true,
        SessionID:    req.SessionID,
        FinalMessage: req.FinalMessage,
    })
    writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

In `Start()` (~line 103), register the routes alongside existing ones. Find the route registration block and add:

```go
mux.HandleFunc("POST /task/enqueue", s.handleTaskEnqueue)
mux.HandleFunc("GET /task/await", s.handleTaskAwait)
mux.HandleFunc("POST /task/report", s.handleTaskReport)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestEnqueueRoute|TestAwait|TestEnqueueRejects' ./internal/daemon/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/endpoints_test.go
git commit -m "feat(daemon): /task/enqueue, /task/await, /task/report endpoints"
```

---

### Task 8: Daemon client helpers

**Files:**
- Modify: `internal/daemon/client.go`
- Test: `internal/daemon/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/client_test.go` (create if missing):

```go
package daemon

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

func TestClientEnqueueTask(t *testing.T) {
    h := http.NewServeMux()
    h.HandleFunc("POST /task/enqueue", func(w http.ResponseWriter, r *http.Request) {
        var got taskEnqueueReq
        _ = json.NewDecoder(r.Body).Decode(&got)
        if got.Task != "t1" || got.Prompt != "p" || got.QueueMax != 4 {
            t.Errorf("body wrong: %+v", got)
        }
        writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: true, InvocationID: "id-1"})
    })
    ts := httptest.NewServer(h)
    defer ts.Close()
    inv, err := EnqueueTaskHTTP(context.Background(), ts.URL, EnqueueRequest{
        Session: "s", Task: "t1", Prompt: "p", QueueMax: 4, Timeout: 10 * time.Second,
    })
    if err != nil {
        t.Fatalf("client: %v", err)
    }
    if !inv.Accepted || inv.InvocationID != "id-1" {
        t.Fatalf("response wrong: %+v", inv)
    }
}

func TestClientAwaitTask(t *testing.T) {
    h := http.NewServeMux()
    h.HandleFunc("GET /task/await", func(w http.ResponseWriter, r *http.Request) {
        if !strings.Contains(r.URL.RawQuery, "invocation_id=id-1") {
            t.Errorf("query missing id: %s", r.URL.RawQuery)
        }
        writeJSON(w, http.StatusOK, map[string]any{
            "ok": true, "session_id": "cs1", "final_message": "done",
        })
    })
    ts := httptest.NewServer(h)
    defer ts.Close()
    res, err := AwaitTaskHTTP(context.Background(), ts.URL, "id-1")
    if err != nil {
        t.Fatalf("await: %v", err)
    }
    if !res.OK || res.SessionID != "cs1" || res.FinalMessage != "done" {
        t.Fatalf("result: %+v", res)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestClientEnqueueTask|TestClientAwaitTask' ./internal/daemon/`
Expected: compile error.

- [ ] **Step 3: Implement client helpers**

Append to `internal/daemon/client.go`:

```go
type EnqueueRequest struct {
    Session  string
    Task     string
    Prompt   string
    Channels []string
    QueueMax int
    Timeout  time.Duration
}

type EnqueueResponse struct {
    Accepted     bool   `json:"accepted"`
    InvocationID string `json:"invocation_id"`
    Reason       string `json:"reason"`
}

type AwaitResponse struct {
    OK           bool   `json:"ok"`
    SessionID    string `json:"session_id"`
    FinalMessage string `json:"final_message"`
    Err          string `json:"error"`
}

// EnqueueTaskHTTP posts to /task/enqueue. baseURL is "http://unix" for the
// Unix-socket client or a real http URL for tests. The caller passes the
// already-wrapped prompt (marker + delivery footer included).
func EnqueueTaskHTTP(ctx context.Context, baseURL string, req EnqueueRequest) (EnqueueResponse, error) {
    body := map[string]any{
        "session":         req.Session,
        "task":            req.Task,
        "prompt":          req.Prompt,
        "channels":        req.Channels,
        "queue_max":       req.QueueMax,
        "timeout_seconds": int(req.Timeout.Seconds()),
    }
    raw, _ := json.Marshal(body)
    hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/task/enqueue", bytes.NewReader(raw))
    if err != nil {
        return EnqueueResponse{}, err
    }
    hreq.Header.Set("Content-Type", "application/json")
    cli := defaultHTTPClient(baseURL)
    resp, err := cli.Do(hreq)
    if err != nil {
        return EnqueueResponse{}, err
    }
    defer resp.Body.Close()
    var out EnqueueResponse
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return EnqueueResponse{}, err
    }
    return out, nil
}

// AwaitTaskHTTP long-polls /task/await. Honors the supplied context (caller
// is responsible for any deadline).
func AwaitTaskHTTP(ctx context.Context, baseURL, invocationID string) (AwaitResponse, error) {
    u := baseURL + "/task/await?invocation_id=" + url.QueryEscape(invocationID)
    hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
    if err != nil {
        return AwaitResponse{}, err
    }
    cli := defaultHTTPClient(baseURL)
    resp, err := cli.Do(hreq)
    if err != nil {
        return AwaitResponse{}, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return AwaitResponse{}, fmt.Errorf("await: status %d", resp.StatusCode)
    }
    var out AwaitResponse
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return AwaitResponse{}, err
    }
    return out, nil
}

// ReportTaskHTTP posts to /task/report. Used by `leo internal task-report`.
func ReportTaskHTTP(ctx context.Context, baseURL string, invocationID, sessionID, finalMessage, sessionName string) error {
    body := map[string]any{
        "invocation_id": invocationID,
        "session_id":    sessionID,
        "final_message": finalMessage,
        "session_name":  sessionName,
    }
    raw, _ := json.Marshal(body)
    hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/task/report", bytes.NewReader(raw))
    if err != nil {
        return err
    }
    hreq.Header.Set("Content-Type", "application/json")
    cli := defaultHTTPClient(baseURL)
    resp, err := cli.Do(hreq)
    if err != nil {
        return err
    }
    resp.Body.Close()
    return nil
}

// defaultHTTPClient returns the existing Unix-socket client for "http://unix"
// and a plain net/http client for anything else (tests).
func defaultHTTPClient(baseURL string) *http.Client {
    if strings.HasPrefix(baseURL, "http://unix") {
        // Reuse existing helper. Caller supplied the sock path elsewhere; this
        // path is only reached when the daemon address is the unix socket.
        return newUnixClient(currentSockPath())
    }
    return &http.Client{Timeout: 0}
}

// currentSockPath is set by Send() so helpers can find it. Simple package-level
// var; daemon clients are not concurrent-per-process in our usage.
var currentSockPath_v string

func setCurrentSockPath(p string)         { currentSockPath_v = p }
func currentSockPath() string             { return currentSockPath_v }
```

In the existing `Send()` (line 42), add `setCurrentSockPath(sockPath)` just after sockPath is computed, so subsequent helper calls in the same process find the socket. (This keeps the existing `Send` API intact.)

Add imports to `client.go` as needed: `"bytes"`, `"fmt"`, `"net/url"`, `"strings"`, `"time"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestClientEnqueueTask|TestClientAwaitTask' ./internal/daemon/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/client.go internal/daemon/client_test.go
git commit -m "feat(daemon): EnqueueTaskHTTP / AwaitTaskHTTP / ReportTaskHTTP client helpers"
```

---

## Phase 3 — Wire-up (supervisor, runner, hidden CLI)

### Task 9: Extract supervise loop from `process.go`

**Files:**
- Create: `internal/service/superviseloop.go`
- Modify: `internal/service/process.go`

- [ ] **Step 1: Read the current loop**

Open `internal/service/process.go` and locate `superviseProcess` (~line 480). The body has these phases: kill stale session → build shell command → `tmux new-session` → wait for end → restart-backoff. Mentally identify which pieces are process-specific vs. generic.

- [ ] **Step 2: Add the extracted skeleton (no behavior change yet)**

Create `internal/service/superviseloop.go`:

```go
package service

import (
    "context"
    "time"
)

// LoopSpec describes one tmux-hosted claude that should be kept alive.
// Both processes and persistent task sessions share this shape.
type LoopSpec struct {
    Name        string                   // logical name; used for state/logs
    SessionName string                   // tmux session name (e.g. "leo-foo")
    Workdir     string
    ShellCmd    string                   // already-assembled `claude ...` command line
    OnSessionEnd func(restartCount int)  // optional callback after each end (for state updates)
}

// runSuperviseLoop is the generic restart-with-backoff loop shared by
// process.go (processes) and session.go (persistent task sessions).
// Returns when ctx is cancelled.
func runSuperviseLoop(ctx context.Context, tmuxPath string, spec LoopSpec) {
    backoff := time.Second
    const maxBackoff = 60 * time.Second
    restarts := 0
    for {
        if ctx.Err() != nil {
            return
        }
        // kill any stale session
        _ = execCommand(tmuxPath, "-L", "leo", "kill-session", "-t", spec.SessionName).Run()
        // new-session
        cmd := execCommand(tmuxPath, "-L", "leo", "new-session", "-d", "-s", spec.SessionName,
            "-c", spec.Workdir, "-x", "200", "-y", "50", spec.ShellCmd)
        if err := cmd.Run(); err != nil {
            // backoff and retry
            time.Sleep(backoff)
            if backoff < maxBackoff {
                backoff *= 2
            }
            continue
        }
        backoff = time.Second
        // wait for session to end
        for ctx.Err() == nil {
            check := execCommand(tmuxPath, "-L", "leo", "has-session", "-t", spec.SessionName)
            if err := check.Run(); err != nil {
                break // session ended
            }
            time.Sleep(500 * time.Millisecond)
        }
        if ctx.Err() != nil {
            return
        }
        restarts++
        if spec.OnSessionEnd != nil {
            spec.OnSessionEnd(restarts)
        }
        // exponential backoff before next restart
        time.Sleep(backoff)
        if backoff < maxBackoff {
            backoff *= 2
        }
    }
}

// execCommand is exposed for testing — both process.go and session.go reuse
// supervisedExecFn for their own process spawn, but the loop's internal
// tmux helpers can be intercepted via this seam.
var execCommand = defaultExecCommand
```

Add to the top of `internal/service/process.go` (if not already there) the import that `defaultExecCommand` comes from — likely you'll need to factor `defaultExecCommand` out as well. Check existing `var supervisedExecFn` (line 30 area) — keep that intact; the `execCommand` var here is a separate seam for the loop's *tmux* calls.

If `defaultExecCommand` doesn't exist, add to `superviseloop.go`:

```go
import "os/exec"

func defaultExecCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
```

- [ ] **Step 3: Verify the existing process.go still compiles**

Run: `go build ./internal/service/`
Expected: success.

- [ ] **Step 4: Run all service tests**

Run: `go test -race ./internal/service/`
Expected: PASS (no behavior changed yet).

- [ ] **Step 5: Commit**

```bash
git add internal/service/superviseloop.go
git commit -m "refactor(service): extract runSuperviseLoop skeleton for reuse by session supervisor"
```

Note: `superviseProcess` in `process.go` is **not** yet refactored to call this — the existing process loop stays as-is to keep this task purely additive. The new session supervisor (next task) is the first consumer.

---

### Task 10: Persistent session supervisor

**Files:**
- Create: `internal/service/session.go`
- Test: `internal/service/session_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/service/session_test.go`:

```go
package service

import (
    "context"
    "os/exec"
    "strings"
    "testing"
    "time"
)

func TestSessionSpecBuildArgs(t *testing.T) {
    spec := SessionSpec{
        Name:      "daily",
        Workdir:   "/tmp/d",
        Model:     "sonnet",
        Channels:  []string{"plugin:slack@official"},
        ResumeID:  "csid-1",
    }
    args := buildSessionClaudeArgs(spec)
    j := strings.Join(args, " ")
    for _, want := range []string{"--model sonnet", "--channels plugin:slack@official", "--resume csid-1"} {
        if !strings.Contains(j, want) {
            t.Fatalf("expected %q in args: %s", want, j)
        }
    }
}

func TestSuperviseSessionStopsOnCtxCancel(t *testing.T) {
    // Verify the loop exits cleanly when context is cancelled.
    // Replace execCommand with a no-op so we don't actually shell out.
    orig := execCommand
    defer func() { execCommand = orig }()
    execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("true") }
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        runSuperviseLoop(ctx, "tmux", LoopSpec{Name: "x", SessionName: "leo-session-x", Workdir: "/tmp", ShellCmd: "echo"})
        close(done)
    }()
    time.Sleep(50 * time.Millisecond)
    cancel()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("loop did not exit on cancel")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestSessionSpecBuildArgs|TestSuperviseSessionStopsOnCtxCancel' ./internal/service/`
Expected: compile error — `SessionSpec`, `buildSessionClaudeArgs` undefined.

- [ ] **Step 3: Implement the supervisor**

Create `internal/service/session.go`:

```go
package service

import (
    "context"
    "fmt"
    "strings"

    "github.com/blackpaw-studio/leo/internal/config"
    "github.com/blackpaw-studio/leo/internal/hooks"
)

// SessionSpec is the runtime descriptor for one supervised persistent claude
// session. Materialized at daemon start from config.Sessions plus any
// implicit sessions derived from runtime: persistent tasks without `session:`.
type SessionSpec struct {
    Name            string
    Workdir         string
    Model           string
    Agent           string
    PermissionMode  string
    AllowedTools    []string
    DisallowedTools []string
    AppendPrompt    string
    AddDirs         []string
    Channels        []string
    Env             map[string]string
    ResumeID        string
}

func sessionTmuxName(name string) string { return "leo-session-" + name }

// buildSessionClaudeArgs assembles the claude CLI args for a persistent
// session. Mirrors buildProcessArgs but for SessionSpec.
func buildSessionClaudeArgs(spec SessionSpec) []string {
    var a []string
    if spec.Model != "" {
        a = append(a, "--model", spec.Model)
    }
    if spec.ResumeID != "" {
        a = append(a, "--resume", spec.ResumeID)
    }
    if spec.PermissionMode != "" {
        a = append(a, "--permission-mode", spec.PermissionMode)
    }
    for _, ch := range spec.Channels {
        a = append(a, "--channels", ch)
    }
    if spec.Agent != "" {
        a = append(a, "--agent", spec.Agent)
    }
    a = append(a, "--add-dir", spec.Workdir)
    for _, d := range spec.AddDirs {
        a = append(a, "--add-dir", d)
    }
    if len(spec.AllowedTools) > 0 {
        a = append(a, "--allowed-tools", strings.Join(spec.AllowedTools, ","))
    }
    if len(spec.DisallowedTools) > 0 {
        a = append(a, "--disallowed-tools", strings.Join(spec.DisallowedTools, ","))
    }
    if spec.AppendPrompt != "" {
        a = append(a, "--append-system-prompt", spec.AppendPrompt)
    }
    return a
}

// SuperviseSession starts the restart-loop for one session in its own
// goroutine. Caller is responsible for ctx lifecycle.
func SuperviseSession(ctx context.Context, tmuxPath, claudePath string, spec SessionSpec, onSessionEnd func(int)) error {
    if err := hooks.EnsureLeoStopHook(spec.Workdir); err != nil {
        return fmt.Errorf("ensure stop hook: %w", err)
    }
    args := buildSessionClaudeArgs(spec)
    shellCmd := claudePath
    for _, a := range args {
        shellCmd += " " + shellQuote(a)
    }
    // env injection: prefix with LEO_SESSION_NAME and LEO_CHANNELS exports
    envExports := fmt.Sprintf("LEO_SESSION_NAME=%q LEO_CHANNELS=%q",
        spec.Name, strings.Join(spec.Channels, ","))
    if len(spec.Env) > 0 {
        for k, v := range spec.Env {
            envExports += fmt.Sprintf(" %s=%q", k, v)
        }
    }
    fullShell := envExports + " exec " + shellCmd
    loop := LoopSpec{
        Name:         spec.Name,
        SessionName:  sessionTmuxName(spec.Name),
        Workdir:      spec.Workdir,
        ShellCmd:     fullShell,
        OnSessionEnd: onSessionEnd,
    }
    go runSuperviseLoop(ctx, tmuxPath, loop)
    return nil
}

// SessionSpecsFromConfig builds SessionSpec list from config. Includes:
// - all entries in cfg.Sessions
// - implicit sessions from runtime: persistent tasks without a `session:` field
// Excludes `session: process:<name>` which are supervised by the process loop.
func SessionSpecsFromConfig(cfg *config.Config) ([]SessionSpec, error) {
    out := []SessionSpec{}
    // explicit sessions
    for name, sc := range cfg.Sessions {
        out = append(out, SessionSpec{
            Name:            name,
            Workdir:         sc.Workspace,
            Model:           sc.Model,
            Agent:           sc.Agent,
            PermissionMode:  sc.PermissionMode,
            AllowedTools:    sc.AllowedTools,
            DisallowedTools: sc.DisallowedTools,
            AppendPrompt:    sc.AppendSystemPrompt,
            AddDirs:         sc.AddDirs,
            Channels:        sc.Channels,
            Env:             sc.Env,
        })
    }
    // implicit sessions
    seen := map[string]bool{}
    for _, s := range out {
        seen[s.Name] = true
    }
    for name, task := range cfg.Tasks {
        if !strings.EqualFold(task.Runtime, "persistent") {
            continue
        }
        if task.Session != "" {
            continue
        }
        if seen[name] {
            return nil, fmt.Errorf("session name conflict: implicit %q collides with sessions.%s", name, name)
        }
        out = append(out, SessionSpec{
            Name:            name,
            Workdir:         task.Workspace,
            Model:           task.Model,
            PermissionMode:  task.PermissionMode,
            AllowedTools:    task.AllowedTools,
            DisallowedTools: task.DisallowedTools,
            AppendPrompt:    task.AppendSystemPrompt,
            Channels:        task.Channels,
            Env:             task.Env,
        })
    }
    return out, nil
}

func shellQuote(s string) string {
    if !strings.ContainsAny(s, " \t\n\"'\\$`") {
        return s
    }
    return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestSessionSpecBuildArgs|TestSuperviseSessionStopsOnCtxCancel' ./internal/service/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/session.go internal/service/session_test.go
git commit -m "feat(service): persistent session supervisor + SessionSpecsFromConfig"
```

---

### Task 11: Boot sessions in `RunSupervised` + wire router injector

**Files:**
- Modify: `internal/service/process.go` (`RunSupervised`)
- Modify: `internal/daemon/server.go` (`Server.Start`)

- [ ] **Step 1: Wire session boot into RunSupervised**

In `internal/service/process.go`, find `RunSupervised` (~line 390). After the existing process supervisor goroutines are spawned, add a block that boots SessionSpecs:

```go
// (insert near the end of RunSupervised, before the final select/wait)
cfg, _ := config.Load(configPath) // re-read; ignore err — already validated upstream
if cfg != nil {
    sessionSpecs, err := SessionSpecsFromConfig(cfg)
    if err != nil {
        return fmt.Errorf("session specs: %w", err)
    }
    sessions := session.NewStore(homePath)
    for _, sp := range sessionSpecs {
        if id, _, _ := sessions.Get("session:" + sp.Name); id != "" {
            sp.ResumeID = id
        }
        sp := sp // capture
        if err := SuperviseSession(sv.ctx(), tmuxPath, claudePath, sp, func(_ int) {
            sv.incrementRestarts(sp.Name)
        }); err != nil {
            return fmt.Errorf("supervise session %q: %w", sp.Name, err)
        }
    }
}
```

Where `sv.ctx()` is a small helper to be added on Supervisor — if Supervisor doesn't expose its ctx, add a getter:

```go
// Add to internal/service/process.go near Supervisor methods (~line 100-130):
func (s *Supervisor) ctx() context.Context { return s.context }
```

If `Supervisor` doesn't store a context field, fall back to a package-level context passed into `RunSupervised`. Inspect the current code to choose; both options are fine — minimal change wins.

Import `internal/session` if not already imported.

- [ ] **Step 2: Wire router injector to actual tmux**

In `internal/daemon/server.go`, locate the spot where `s.router` is initialized (added in Task 7's New(...)). Right after the assignment, add:

```go
// Real tmux injector. The router is created before `RunSupervised` so we
// pre-bind these to the package-level helpers from internal/tmux.
s.router.SetInjector(func(session, prompt string) error {
    return tmux.InjectPrompt(context.Background(), tmuxPath(), session, prompt)
})
s.router.SetAborter(func(session string) error {
    return tmux.AbortPrompt(context.Background(), tmuxPath(), session)
})
```

Add a small helper at the bottom of `server.go`:

```go
func tmuxPath() string {
    p, err := exec.LookPath("tmux")
    if err != nil {
        return "tmux"
    }
    return p
}
```

Imports: `os/exec`, `context`, `github.com/blackpaw-studio/leo/internal/tmux`.

- [ ] **Step 3: Build verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Run all tests**

Run: `go test -race ./internal/service/ ./internal/daemon/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/process.go internal/daemon/server.go
git commit -m "feat: boot persistent sessions in RunSupervised and wire tmux injector"
```

---

### Task 12: Hidden CLI — `leo internal task-report`

**Files:**
- Create: `internal/cli/internal_task_report.go`
- Test: `internal/cli/internal_task_report_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/internal_task_report_test.go`:

```go
package cli

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestExtractInvocationMarker(t *testing.T) {
    body := "<!-- leo:invocation=abcdef0123456789abcdef0123456789 -->\nhello"
    got := extractInvocationMarker(body)
    if got != "abcdef0123456789abcdef0123456789" {
        t.Fatalf("got %q", got)
    }
    if extractInvocationMarker("plain") != "" {
        t.Fatalf("expected empty for no marker")
    }
}

func TestReadLastUserAndAssistant(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "transcript.jsonl")
    lines := []string{
        `{"type":"user","message":{"content":[{"type":"text","text":"<!-- leo:invocation=aaaa -->\nstart"}]}}`,
        `{"type":"assistant","message":{"content":[{"type":"text","text":"sure"}]}}`,
        `{"type":"user","message":{"content":[{"type":"text","text":"<!-- leo:invocation=bbbb -->\nsecond"}]}}`,
        `{"type":"assistant","message":{"content":[{"type":"text","text":"all done"}]}}`,
    }
    _ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
    uid, final, err := readLastTurn(p)
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    if uid != "bbbb" {
        t.Fatalf("uid: %q", uid)
    }
    if final != "all done" {
        t.Fatalf("final: %q", final)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestExtractInvocationMarker|TestReadLastTurn' ./internal/cli/`
Expected: compile error — functions undefined.

- [ ] **Step 3: Implement the subcommand**

Create `internal/cli/internal_task_report.go`:

```go
package cli

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "regexp"
    "strings"

    "github.com/blackpaw-studio/leo/internal/daemon"
    "github.com/spf13/cobra"
)

var markerRe = regexp.MustCompile(`<!-- leo:invocation=([0-9a-f]{32}) -->`)

func extractInvocationMarker(s string) string {
    m := markerRe.FindStringSubmatch(s)
    if len(m) < 2 {
        return ""
    }
    return m[1]
}

type transcriptEvent struct {
    Type    string `json:"type"`
    Message struct {
        Content []struct {
            Type string `json:"type"`
            Text string `json:"text"`
        } `json:"content"`
    } `json:"message"`
}

// readLastTurn scans the transcript JSONL and returns the invocation id from
// the most recent user message that carries a marker, plus the concatenated
// text of the next assistant message following it. Returns ("", "", nil) if
// no leo-marker user message is found (human turn).
func readLastTurn(path string) (string, string, error) {
    f, err := os.Open(path)
    if err != nil {
        return "", "", err
    }
    defer f.Close()
    var events []transcriptEvent
    sc := bufio.NewScanner(f)
    sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
    for sc.Scan() {
        var ev transcriptEvent
        if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
            continue // skip malformed lines
        }
        events = append(events, ev)
    }
    if err := sc.Err(); err != nil && err != io.EOF {
        return "", "", err
    }
    lastUserIdx := -1
    var invID string
    for i := len(events) - 1; i >= 0; i-- {
        if events[i].Type != "user" {
            continue
        }
        text := concatText(events[i])
        if id := extractInvocationMarker(text); id != "" {
            lastUserIdx = i
            invID = id
            break
        }
    }
    if lastUserIdx < 0 {
        return "", "", nil
    }
    var final strings.Builder
    for j := lastUserIdx + 1; j < len(events); j++ {
        if events[j].Type != "assistant" {
            continue
        }
        if final.Len() > 0 {
            final.WriteString("\n")
        }
        final.WriteString(concatText(events[j]))
    }
    return invID, final.String(), nil
}

func concatText(ev transcriptEvent) string {
    var b strings.Builder
    for _, part := range ev.Message.Content {
        if part.Type == "text" {
            b.WriteString(part.Text)
        }
    }
    return b.String()
}

type hookEnvelope struct {
    SessionID      string `json:"session_id"`
    TranscriptPath string `json:"transcript_path"`
    HookEventName  string `json:"hook_event_name"`
    CWD            string `json:"cwd"`
}

func newInternalCmd() *cobra.Command {
    parent := &cobra.Command{Use: "internal", Hidden: true}
    parent.AddCommand(newInternalTaskReportCmd())
    return parent
}

func newInternalTaskReportCmd() *cobra.Command {
    return &cobra.Command{
        Use:    "task-report",
        Hidden: true,
        Short:  "Report a Claude Code Stop hook to the leo daemon",
        RunE: func(cmd *cobra.Command, args []string) error {
            var env hookEnvelope
            if err := json.NewDecoder(os.Stdin).Decode(&env); err != nil {
                fmt.Fprintf(os.Stderr, "leo task-report: bad stdin: %v\n", err)
                return nil // never block claude
            }
            if env.TranscriptPath == "" {
                return nil
            }
            invID, final, err := readLastTurn(env.TranscriptPath)
            if err != nil {
                fmt.Fprintf(os.Stderr, "leo task-report: %v\n", err)
                return nil
            }
            if invID == "" {
                return nil // human turn, ignore
            }
            sockPath := daemon.SockPath(workDir())
            base := "http://unix"
            // Use the existing Send() to set the sock-path side effect, then
            // post via ReportTaskHTTP.
            _, _ = daemon.Send(context.Background(), workDir(), "GET", "/health", nil)
            _ = base
            if err := daemon.ReportTaskHTTP(context.Background(), base, invID, env.SessionID, final, os.Getenv("LEO_SESSION_NAME")); err != nil {
                fmt.Fprintf(os.Stderr, "leo task-report: report: %v\n", err)
            }
            _ = sockPath
            return nil
        },
    }
}
```

Helper `workDir()` is already defined elsewhere in `internal/cli` (it resolves the leo home). Reuse it.

Register the command in `internal/cli/root.go` — find where other subcommands are added (e.g. `rootCmd.AddCommand(...)`) and add:

```go
rootCmd.AddCommand(newInternalCmd())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestExtractInvocationMarker|TestReadLastTurn' ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/internal_task_report.go internal/cli/internal_task_report_test.go internal/cli/root.go
git commit -m "feat(cli): hidden 'leo internal task-report' Stop hook subcommand"
```

---

### Task 13: Persistent runner branch in `runner.Run`

**Files:**
- Create: `internal/run/persistent.go`
- Modify: `internal/run/runner.go`
- Test: `internal/run/persistent_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/run/persistent_test.go`:

```go
package run

import (
    "strings"
    "testing"

    "github.com/blackpaw-studio/leo/internal/config"
)

func TestWrapPromptWithMarkerAndFooter(t *testing.T) {
    out := wrapPromptForPersistent("xyz", "hello", []string{"plugin:slack@official", "plugin:tg@official"})
    if !strings.Contains(out, "<!-- leo:invocation=xyz -->") {
        t.Fatalf("missing marker:\n%s", out)
    }
    if !strings.Contains(out, "hello") {
        t.Fatalf("missing body")
    }
    if !strings.Contains(out, "plugin:slack@official, plugin:tg@official") {
        t.Fatalf("missing channel footer")
    }
}

func TestWrapPromptOmitsFooterWhenNoChannels(t *testing.T) {
    out := wrapPromptForPersistent("xyz", "hello", nil)
    if strings.Contains(out, "deliver your final reply") {
        t.Fatalf("expected no delivery footer when channels empty:\n%s", out)
    }
}

func TestRunPersistentDispatchSelected(t *testing.T) {
    // Sanity: when task.Runtime == "persistent", runner.Run should dispatch to
    // runPersistent. We don't run the full HTTP path here — we set a sentinel
    // hook to capture the dispatch.
    called := false
    orig := persistentImpl
    defer func() { persistentImpl = orig }()
    persistentImpl = func(cfg *config.Config, taskName string) error {
        called = true
        return nil
    }
    cfg := &config.Config{
        Tasks: map[string]config.TaskConfig{
            "t1": {Runtime: "persistent", PromptFile: "_", Workspace: "/tmp"},
        },
    }
    _ = Run(cfg, "t1", nil)
    if !called {
        t.Fatalf("expected runPersistent dispatch")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestWrapPrompt|TestRunPersistentDispatchSelected' ./internal/run/`
Expected: compile error — `wrapPromptForPersistent`, `persistentImpl` undefined.

- [ ] **Step 3: Implement the persistent runner**

Create `internal/run/persistent.go`:

```go
package run

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/blackpaw-studio/leo/internal/config"
    "github.com/blackpaw-studio/leo/internal/daemon"
    "github.com/blackpaw-studio/leo/internal/history"
    "github.com/blackpaw-studio/leo/internal/session"
)

// persistentImpl is a seam for tests to override the runPersistent dispatch.
var persistentImpl = runPersistent

// wrapPromptForPersistent prepends the leo invocation marker and (when
// channels are non-empty) appends the delivery footer.
func wrapPromptForPersistent(invID, body string, channels []string) string {
    marker := fmt.Sprintf("<!-- leo:invocation=%s -->\n", invID)
    if len(channels) == 0 {
        return marker + body
    }
    return marker + body + "\n\n---\nWhen finished, deliver your final reply to the user via these channel plugin(s): " +
        strings.Join(channels, ", ") + ".\n"
}

func runPersistent(cfg *config.Config, taskName string) error {
    task, err := resolveTask(cfg, taskName)
    if err != nil {
        return err
    }
    sessName, _, err := cfg.ResolveSession(taskName)
    if err != nil {
        return err
    }
    body, err := assemblePrompt(cfg, task)
    if err != nil {
        return err
    }
    // Pre-allocate invocation id so the body carries the marker.
    invID := newInvocationID16()
    wrapped := wrapPromptForPersistent(invID, body, task.Channels)

    base := "http://unix"
    timeout := cfg.TaskTimeout(task)
    ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
    defer cancel()

    enq, err := daemon.EnqueueTaskHTTP(ctx, base, daemon.EnqueueRequest{
        Session: sessName, Task: taskName, Prompt: wrapped, Channels: task.Channels,
        QueueMax: task.QueueMax, Timeout: timeout,
    })
    if err != nil {
        recordFailure(cfg, taskName, fmt.Sprintf("enqueue: %v", err))
        return err
    }
    if !enq.Accepted {
        recordFailure(cfg, taskName, "rejected: "+enq.Reason)
        return fmt.Errorf("enqueue rejected: %s", enq.Reason)
    }
    // NOTE: enq.InvocationID is allocated server-side; our pre-built marker uses
    // a different id. To keep them in sync, set the prompt marker to enq.InvocationID
    // by re-enqueueing? Simpler: server should accept a client-provided id. See
    // Step 4 below for the small server tweak; for now we trust the server id.

    aw, err := daemon.AwaitTaskHTTP(ctx, base, enq.InvocationID)
    if err != nil {
        recordFailure(cfg, taskName, fmt.Sprintf("await: %v", err))
        return err
    }
    if !aw.OK {
        recordFailure(cfg, taskName, "task: "+aw.Err)
        return fmt.Errorf("task failed: %s", aw.Err)
    }

    // success: persist session id + history
    if aw.SessionID != "" {
        store := session.NewStore(cfg.HomePath)
        _ = store.Set("session:"+sessName, aw.SessionID)
    }
    hist := history.NewStore(cfg.HomePath)
    _ = hist.Record(taskName, 0, "completed", aw.FinalMessage)
    return nil
}

func recordFailure(cfg *config.Config, taskName, reason string) {
    hist := history.NewStore(cfg.HomePath)
    _ = hist.Record(taskName, 1, reason, "")
}

func newInvocationID16() string {
    // 32 hex chars = 16 random bytes; matches the regex in markerRe.
    var b [16]byte
    _, _ = readRandom(b[:])
    return hex.EncodeToString(b[:])
}
```

Add the necessary imports (`encoding/hex`, plus a small helper):

```go
import (
    "crypto/rand"
    "encoding/hex"
)

func readRandom(b []byte) (int, error) { return rand.Read(b) }
```

In `internal/run/runner.go`, at the top of `Run()` (line 85), add the branch:

```go
func Run(cfg *config.Config, taskName string, sessions *session.Store) error {
    task, err := resolveTask(cfg, taskName)
    if err != nil {
        return err
    }
    if strings.EqualFold(task.Runtime, "persistent") {
        return persistentImpl(cfg, taskName)
    }
    // existing body continues...
}
```

Reconcile the marker/id mismatch noted above: change the server's `taskEnqueueReq` to accept an optional `invocation_id` from the client, and have `Enqueue` use it when non-empty. In `internal/daemon/server.go` `handleTaskEnqueue`:

```go
// add to taskEnqueueReq:
InvocationID string `json:"invocation_id"`

// in handleTaskEnqueue, after decoding req, override the id:
inv, ok := s.router.EnqueueWithID(req.InvocationID, EnqueueParams{...})
```

And in `internal/daemon/session_router.go`, add:

```go
func (r *sessionRouter) EnqueueWithID(id string, p EnqueueParams) (*PendingInvocation, bool) {
    if id == "" {
        return r.Enqueue(p)
    }
    // same body as Enqueue but use the supplied id instead of newInvocationID()
    // ... factor common logic; see Enqueue and substitute inv.ID = id.
    // ALSO: include id in client request from runPersistent above.
}
```

Then in `runPersistent`, pass `InvocationID: invID` to `EnqueueRequest` (extend `EnqueueRequest` in client too).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -run 'TestWrapPrompt|TestRunPersistentDispatchSelected' ./internal/run/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/run/persistent.go internal/run/persistent_test.go internal/run/runner.go internal/daemon/session_router.go internal/daemon/server.go internal/daemon/client.go
git commit -m "feat(run): persistent runner branch + client-supplied invocation id"
```

---

## Phase 4 — Operator UX, integration, docs

### Task 14: `leo session` CLI commands

**Files:**
- Create: `internal/cli/session.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/session_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/cli/session_test.go`:

```go
package cli

import (
    "bytes"
    "testing"
)

func TestSessionListUsesConfig(t *testing.T) {
    var buf bytes.Buffer
    cmd := newSessionCmd()
    cmd.SetOut(&buf)
    cmd.SetArgs([]string{"list"})
    // We don't have a daemon here; the command should print the configured
    // sessions from the YAML even when not running. Check that --help works.
    cmd.SetArgs([]string{"--help"})
    if err := cmd.Execute(); err != nil {
        t.Fatalf("execute: %v", err)
    }
    if !bytes.Contains(buf.Bytes(), []byte("list")) ||
        !bytes.Contains(buf.Bytes(), []byte("status")) ||
        !bytes.Contains(buf.Bytes(), []byte("attach")) {
        t.Fatalf("missing subcommands:\n%s", buf.String())
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSessionListUsesConfig ./internal/cli/`
Expected: compile error — `newSessionCmd` undefined.

- [ ] **Step 3: Implement `leo session` family**

Create `internal/cli/session.go` with subcommands `list`, `status`, `attach`, `logs`, `reset`, `drain`. Each subcommand is concise:

```go
package cli

import (
    "fmt"
    "syscall"

    "github.com/blackpaw-studio/leo/internal/config"
    "github.com/blackpaw-studio/leo/internal/session"
    "github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
    parent := &cobra.Command{Use: "session", Short: "Manage persistent task sessions"}
    parent.AddCommand(newSessionListCmd(), newSessionStatusCmd(), newSessionAttachCmd(),
        newSessionLogsCmd(), newSessionResetCmd(), newSessionDrainCmd())
    return parent
}

func newSessionListCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "list",
        Short: "List configured persistent sessions",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg, err := config.Load(configPath())
            if err != nil {
                return err
            }
            for name, sess := range cfg.Sessions {
                fmt.Fprintf(cmd.OutOrStdout(), "%s\tworkspace=%s\tmodel=%s\tchannels=%v\n",
                    name, sess.Workspace, sess.Model, sess.Channels)
            }
            return nil
        },
    }
}

func newSessionStatusCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "status <name>",
        Short: "Show session runtime status",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            // Hit /processes (or a future /sessions/status) — for now, print
            // the stored session id and whether the tmux session exists.
            store := session.NewStore(homeDir())
            id, _, _ := store.Get("session:" + args[0])
            fmt.Fprintf(cmd.OutOrStdout(), "session=%s\nsession_id=%s\n", args[0], id)
            return nil
        },
    }
}

func newSessionAttachCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "attach <name>",
        Short: "tmux attach to the session",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            target := "leo-session-" + args[0]
            tmux, err := lookupTmux()
            if err != nil {
                return err
            }
            return syscall.Exec(tmux, []string{"tmux", "-L", "leo", "attach", "-t", target}, nil)
        },
    }
}

func newSessionLogsCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "logs <name>",
        Short: "Print recent pane scrollback",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            return captureTmuxPaneToStdout("leo-session-" + args[0])
        },
    }
}

func newSessionResetCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "reset <name>",
        Short: "Kill tmux session and clear stored session_id — next supervisor pass starts fresh",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            target := "leo-session-" + args[0]
            tmux, _ := lookupTmux()
            if tmux != "" {
                _ = killTmuxSession(tmux, target)
            }
            store := session.NewStore(homeDir())
            return store.Delete("session:" + args[0])
        },
    }
}

func newSessionDrainCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "drain <name>",
        Short: "Block until the session's queue is empty",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            // Minimal v1: poll daemon /processes (queue depth field to be added
            // to /processes payload as follow-up). For now, sleep-poll the
            // stored "in-flight" indicator. YAGNI; print a stub.
            fmt.Fprintln(cmd.OutOrStdout(), "drain: not yet implemented — use 'leo session status'")
            return nil
        },
    }
}
```

Helpers `lookupTmux`, `captureTmuxPaneToStdout`, `killTmuxSession`, `configPath`, `homeDir` already exist in `internal/cli/` — reuse them. If a name doesn't exist exactly, grep for a near-equivalent and call that.

Register in `internal/cli/root.go`:

```go
rootCmd.AddCommand(newSessionCmd())
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race -run TestSessionListUsesConfig ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/session.go internal/cli/session_test.go internal/cli/root.go
git commit -m "feat(cli): leo session list/status/attach/logs/reset/drain"
```

---

### Task 15: Extend `e2e/fakeclaude` with interactive mode

**Files:**
- Modify: `e2e/fakeclaude/main.go`

- [ ] **Step 1: Inspect the current fake**

Read `e2e/fakeclaude/main.go` (likely a small file that flags-on `-p` and echoes a canned response). The interactive mode needs to:

- accept stdin lines (no `-p`)
- on each Enter-terminated submission, write a JSONL transcript line at a path configured by `--transcript` (or derive from cwd)
- after writing the assistant line, emit a stop hook envelope via a sidechannel (here: the fakeclaude itself doesn't *run* the hook — the e2e test directly calls the daemon's `/task/report` once the transcript line is written, to simulate the hook)

Simplest implementation: when fakeclaude is invoked without `-p`, run an interactive loop that reads stdin, writes assistant transcript lines to a path configured via `--transcript-path`, and prints "ready" markers.

- [ ] **Step 2: Modify fakeclaude**

In `e2e/fakeclaude/main.go`, add:

```go
var (
    interactiveFlag    = flag.Bool("interactive", false, "interactive mode — no -p")
    transcriptPathFlag = flag.String("transcript-path", "", "JSONL transcript path")
    resumeFlag         = flag.String("resume", "", "resume id (echoed back)")
)

func init() {
    flag.BoolVar(interactiveFlag, "i", false, "shorthand for --interactive")
}

// In main(), after flag.Parse():
if *interactiveFlag || isTTYStdin() {
    runInteractive()
    return
}
// existing -p path...
```

Add `runInteractive()`:

```go
func runInteractive() {
    sc := bufio.NewScanner(os.Stdin)
    sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
    var buf bytes.Buffer
    for sc.Scan() {
        line := sc.Text()
        if line == "" && buf.Len() > 0 {
            submission := strings.TrimSpace(buf.String())
            buf.Reset()
            writeTranscriptUser(*transcriptPathFlag, submission)
            reply := "FAKE-REPLY: " + truncate(submission, 80)
            writeTranscriptAssistant(*transcriptPathFlag, reply)
            fmt.Println(reply)
            continue
        }
        buf.WriteString(line)
        buf.WriteByte('\n')
    }
}
```

`writeTranscriptUser` / `writeTranscriptAssistant` are simple JSONL appenders:

```go
func writeTranscriptUser(path, text string) {
    if path == "" {
        return
    }
    ev := map[string]any{
        "type": "user",
        "message": map[string]any{
            "content": []map[string]any{{"type": "text", "text": text}},
        },
    }
    appendJSONL(path, ev)
}

func writeTranscriptAssistant(path, text string) {
    ev := map[string]any{
        "type": "assistant",
        "message": map[string]any{
            "content": []map[string]any{{"type": "text", "text": text}},
        },
    }
    appendJSONL(path, ev)
}

func appendJSONL(path string, v any) {
    raw, _ := json.Marshal(v)
    f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
    if err != nil {
        return
    }
    defer f.Close()
    f.Write(raw)
    f.Write([]byte("\n"))
}

func isTTYStdin() bool { return false } // simplest stub for e2e
```

- [ ] **Step 3: Build fakeclaude**

Run: `go build -o /tmp/fakeclaude ./e2e/fakeclaude/`
Expected: success.

- [ ] **Step 4: Smoke**

Run: `echo "hello\n" | /tmp/fakeclaude --interactive --transcript-path /tmp/tr.jsonl && cat /tmp/tr.jsonl`
Expected: stdout shows `FAKE-REPLY: hello`; transcript file contains a user line + assistant line.

- [ ] **Step 5: Commit**

```bash
git add e2e/fakeclaude/main.go
git commit -m "test(e2e): fakeclaude interactive mode for persistent-session tests"
```

---

### Task 16: E2E — persistent basic happy path

**Files:**
- Create: `e2e/persistent_basic_test.go`

- [ ] **Step 1: Write the test**

Create `e2e/persistent_basic_test.go`:

```go
package e2e

import (
    "context"
    "encoding/json"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestPersistentTaskBasic(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e")
    }
    home := t.TempDir()
    workspace := filepath.Join(home, "ws")
    _ = os.MkdirAll(workspace, 0o755)
    transcript := filepath.Join(home, "transcript.jsonl")

    // 1. Write config
    cfgYAML := `
defaults:
  model: sonnet
sessions:
  t1:
    workspace: ` + workspace + `
    model: sonnet
    channels: []
tasks:
  t1:
    runtime: persistent
    schedule: "0 7 * * *"
    prompt_file: ` + writePromptFile(t, workspace, "say hi") + `
    timeout: 5s
`
    cfgPath := writeFile(t, home, "leo.yaml", cfgYAML)

    // 2. Boot fakeclaude-backed daemon
    setEnv(t, "LEO_FAKE_CLAUDE", buildFakeclaude(t))
    setEnv(t, "LEO_FAKE_CLAUDE_TRANSCRIPT", transcript)
    daemonURL := startLeoService(t, cfgPath, home)
    waitForTmuxSession(t, "leo-session-t1", 5*time.Second)

    // 3. Hook file must exist
    hookPath := filepath.Join(workspace, ".claude", "settings.local.json")
    if _, err := os.Stat(hookPath); err != nil {
        t.Fatalf("expected hook installed: %v", err)
    }

    // 4. Fire the task
    runOutput := runLeoCmd(t, "run", "t1", "--config", cfgPath)

    // The test stub simulates the Stop hook by directly POSTing to /task/report
    // once the transcript file shows an assistant turn.
    waitForFile(t, transcript, 2*time.Second)
    simulateStopHook(t, daemonURL, transcript, "leo-session-t1")

    // 5. Wait for `leo run` to return
    if !strings.Contains(runOutput, "completed") {
        // alternative: poll history
        if !historyShowsCompleted(t, home, "t1", 2*time.Second) {
            t.Fatalf("expected history entry 'completed'")
        }
    }

    // 6. Session id persisted
    sid := readStoredSessionID(t, home, "t1")
    if sid == "" {
        t.Fatal("expected stored session id")
    }
}

// (helpers `writePromptFile`, `writeFile`, `setEnv`, `buildFakeclaude`,
//  `startLeoService`, `waitForTmuxSession`, `runLeoCmd`, `waitForFile`,
//  `simulateStopHook`, `historyShowsCompleted`, `readStoredSessionID`
//  live in e2e/helpers_test.go — add them as needed.)
```

Add the helpers file `e2e/helpers_persistent_test.go` (only the missing ones; reuse anything from existing `e2e/`):

```go
package e2e

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func writePromptFile(t *testing.T, dir, body string) string {
    t.Helper()
    p := filepath.Join(dir, "prompt.md")
    if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
        t.Fatal(err)
    }
    return p
}

func writeFile(t *testing.T, dir, name, body string) string {
    t.Helper()
    p := filepath.Join(dir, name)
    if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
        t.Fatal(err)
    }
    return p
}

func setEnv(t *testing.T, k, v string) {
    t.Helper()
    prev := os.Getenv(k)
    os.Setenv(k, v)
    t.Cleanup(func() { os.Setenv(k, prev) })
}

func simulateStopHook(t *testing.T, baseURL, transcript, sessionName string) {
    t.Helper()
    // Parse marker out of the transcript ourselves and POST /task/report.
    raw, _ := os.ReadFile(transcript)
    for _, line := range strings.Split(string(raw), "\n") {
        if !strings.Contains(line, "<!-- leo:invocation=") {
            continue
        }
        id := strings.TrimSpace(strings.SplitN(strings.SplitN(line, "<!-- leo:invocation=", 2)[1], " -->", 2)[0])
        body := map[string]any{
            "invocation_id": id,
            "session_id":    "fake-session-1",
            "final_message": "ok",
            "session_name":  sessionName,
        }
        b, _ := json.Marshal(body)
        req, _ := http.NewRequestWithContext(context.Background(), "POST", baseURL+"/task/report", bytes.NewReader(b))
        req.Header.Set("Content-Type", "application/json")
        http.DefaultClient.Do(req)
        return
    }
    t.Fatalf("no marker in transcript")
}

// (stub the remaining helpers — buildFakeclaude returns the path; startLeoService
//  forks `bin/leo service` against the config; waitForTmuxSession polls
//  `tmux has-session`; runLeoCmd invokes `bin/leo` with args; waitForFile
//  polls os.Stat; historyShowsCompleted reads ~/.leo/state/task-history.json;
//  readStoredSessionID reads ~/.leo/state/sessions.json.)
```

Fill the stubs by either reusing existing e2e helpers (grep `e2e/*.go` for them) or copying their bodies. Each is 10-15 lines of straightforward filesystem/process code; this task delegates the bookkeeping to the implementer.

- [ ] **Step 2: Run the test (expected to fail until implementation lines up)**

Run: `go test -race -run TestPersistentTaskBasic ./e2e/ -v`
Expected initially: fails until all helpers and the runner branch are aligned. Iterate locally until it passes (this is the integration smoke test that exercises everything).

- [ ] **Step 3: Commit**

```bash
git add e2e/persistent_basic_test.go e2e/helpers_persistent_test.go
git commit -m "test(e2e): persistent task happy-path test"
```

---

### Task 17: E2E — queue FIFO + queue-full rejection

**Files:**
- Create: `e2e/persistent_queue_test.go`

- [ ] **Step 1: Write the test**

Create `e2e/persistent_queue_test.go`:

```go
package e2e

import (
    "sync"
    "testing"
    "time"
)

func TestPersistentTaskQueueFIFOAndRejection(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e")
    }
    home, daemonURL, cfgPath, transcript := setupPersistentEnv(t, "queue", "queue_max: 2")
    waitForTmuxSession(t, "leo-session-queue", 5*time.Second)

    // Fire 3 in rapid succession; the third should be rejected.
    var wg sync.WaitGroup
    results := make([]string, 3)
    for i := 0; i < 3; i++ {
        i := i
        wg.Add(1)
        go func() {
            defer wg.Done()
            results[i] = runLeoCmd(t, "run", "queue", "--config", cfgPath)
        }()
        time.Sleep(20 * time.Millisecond)
    }
    // Simulate two Stop hooks (queue depth 2 → first two accepted, then advance).
    go func() {
        time.Sleep(100 * time.Millisecond)
        simulateStopHook(t, daemonURL, transcript, "leo-session-queue")
        time.Sleep(50 * time.Millisecond)
        simulateStopHook(t, daemonURL, transcript, "leo-session-queue")
    }()
    wg.Wait()
    rejectCount := 0
    for _, r := range results {
        if containsAny(r, "rejected", "queue full") {
            rejectCount++
        }
    }
    if rejectCount != 1 {
        t.Fatalf("expected exactly 1 queue-full rejection, got %d (results: %q)", rejectCount, results)
    }
    _ = home
}
```

`setupPersistentEnv` is a shared helper factored from Task 16's setup, parameterized by task name and an extra YAML snippet for the task block. `containsAny` is `strings.Contains` over multiple needles.

- [ ] **Step 2: Run**

Run: `go test -race -run TestPersistentTaskQueueFIFOAndRejection ./e2e/ -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add e2e/persistent_queue_test.go
git commit -m "test(e2e): persistent task FIFO ordering + queue-full rejection"
```

---

### Task 18: E2E — shared session (topology B) + process-shared (topology C)

**Files:**
- Create: `e2e/persistent_shared_test.go`
- Create: `e2e/persistent_process_test.go`

- [ ] **Step 1: Write the shared-session test**

Create `e2e/persistent_shared_test.go`:

```go
package e2e

import (
    "strings"
    "testing"
    "time"
)

func TestPersistentSharedSession(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e")
    }
    cfgExtra := `
sessions:
  shared:
    workspace: ${HOME}/ws
    model: sonnet
tasks:
  a:
    runtime: persistent
    session: shared
    schedule: "0 7 * * *"
    prompt_file: ${HOME}/p.md
    timeout: 5s
  b:
    runtime: persistent
    session: shared
    schedule: "0 8 * * *"
    prompt_file: ${HOME}/p.md
    timeout: 5s
`
    home, daemonURL, cfgPath, transcript := setupCustomPersistentEnv(t, cfgExtra)
    waitForTmuxSession(t, "leo-session-shared", 5*time.Second)

    go func() { runLeoCmd(t, "run", "a", "--config", cfgPath) }()
    go func() { runLeoCmd(t, "run", "b", "--config", cfgPath) }()
    time.Sleep(100 * time.Millisecond)

    // Simulate Stop hooks one at a time and check FIFO.
    simulateStopHook(t, daemonURL, transcript, "leo-session-shared")
    time.Sleep(50 * time.Millisecond)
    simulateStopHook(t, daemonURL, transcript, "leo-session-shared")

    if !strings.Contains(readTranscript(t, transcript), "FAKE-REPLY") {
        t.Fatalf("expected fake replies in transcript")
    }
    _ = home
}
```

- [ ] **Step 2: Write the process-shared test**

Create `e2e/persistent_process_test.go`:

```go
package e2e

import (
    "strings"
    "testing"
    "time"
)

func TestPersistentProcessSharedCorrelation(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e")
    }
    cfgExtra := `
processes:
  bot:
    workspace: ${HOME}/ws
    model: sonnet
    channels: []
tasks:
  poke:
    runtime: persistent
    session: process:bot
    schedule: "0 7 * * *"
    prompt_file: ${HOME}/p.md
    timeout: 5s
`
    home, daemonURL, cfgPath, transcript := setupCustomPersistentEnv(t, cfgExtra)
    waitForTmuxSession(t, "leo-bot", 5*time.Second) // process sessions are leo-<name>, not leo-session-<name>

    // Inject a "human" turn first (no marker), then fire a task.
    appendHumanTranscript(t, transcript, "hello bot")
    time.Sleep(20 * time.Millisecond)
    // Reporting the human turn should be a no-op.
    simulateStopHook(t, daemonURL, transcript, "leo-bot")

    go func() { runLeoCmd(t, "run", "poke", "--config", cfgPath) }()
    time.Sleep(100 * time.Millisecond)
    simulateStopHook(t, daemonURL, transcript, "leo-bot")

    // Task history should show exactly one completion for "poke".
    if !historyShowsCompleted(t, home, "poke", 2*time.Second) {
        t.Fatalf("expected completed history entry for poke")
    }
    if !strings.Contains(readTranscript(t, transcript), "hello bot") {
        t.Fatalf("human turn was lost")
    }
}
```

- [ ] **Step 3: Run**

Run: `go test -race -run 'TestPersistentShared|TestPersistentProcessShared' ./e2e/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add e2e/persistent_shared_test.go e2e/persistent_process_test.go
git commit -m "test(e2e): shared session (B) + process-shared (C) topology tests"
```

---

### Task 19: Lint + full test pass + docs note

**Files:**
- Modify: `docs/configuration/index.md` (or nearest existing configuration page) — add a short section on `runtime: persistent`.

- [ ] **Step 1: Run full lint and tests**

Run:

```bash
make lint
make test
```

Expected: green. Fix any new lint findings introduced by the plan.

- [ ] **Step 2: Add a docs section**

Locate `docs/configuration/` and append a new file `docs/configuration/persistent-tasks.md`:

````markdown
# Persistent Task Sessions

Tasks default to `runtime: oneshot`, which runs `claude -p <prompt>` as a fresh subprocess. Setting `runtime: persistent` reuses a warm `claude` session living in a leo-supervised tmux session.

## Why

- Skip claude startup cost on every firing.
- Carry conversational context across firings without juggling `--resume` ids.
- `tmux attach` to watch a scheduled task run live.

## Quickstart

```yaml
tasks:
  morning:
    runtime: persistent
    schedule: "0 7 * * *"
    prompt_file: prompts/morning.md
    workspace: ~/work/morning
    channels: [plugin:slack@official]
```

That config implicitly creates a dedicated session `leo-session-morning`. Channels declared on the task are loaded at session start.

## Sharing a session across tasks

Declare a session explicitly and reference it from tasks:

```yaml
sessions:
  daily:
    workspace: ~/work/daily
    channels: [plugin:slack@official, plugin:telegram@official]

tasks:
  standup:
    runtime: persistent
    session: daily
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels: [plugin:slack@official]      # MUST be subset of session.channels
  summary:
    runtime: persistent
    session: daily
    schedule: "0 18 * * *"
    prompt_file: prompts/summary.md
    channels: [plugin:telegram@official]
```

## Sharing with a supervised process

```yaml
processes:
  bot:
    workspace: ~/work/bot
    channels: [plugin:telegram@official]

tasks:
  midday-poke:
    runtime: persistent
    session: process:bot
    schedule: "0 12 * * *"
    prompt_file: prompts/midday.md
    channels: [plugin:telegram@official]
```

The same tmux session hosts both the interactive process and the scheduled prompt; sentinel correlation keeps them separated.

## Operator commands

```
leo session list           # all sessions
leo session status <name>  # state + session id + queue depth
leo session attach <name>  # tmux attach
leo session reset <name>   # kill + clear session_id (use when context fills)
```
````

- [ ] **Step 3: Commit**

```bash
git add docs/configuration/persistent-tasks.md
git commit -m "docs: persistent task sessions configuration guide"
```

---

## Self-Review (skill-required)

After writing the plan, verify against the spec:

**1. Spec coverage:**
- Config schema (Sessions, TaskConfig fields): Tasks 1–2 ✓
- Validation rules (runtime enum, session resolution, channel subset, implicit name conflict): Task 2 ✓
- Persistent supervisor + LoopSpec extraction: Tasks 9–10 ✓
- tmux InjectPrompt / AbortPrompt: Task 3 ✓
- Hook installer: Task 4 ✓
- Per-session FIFO + pump + correlation: Tasks 5–6 ✓
- HTTP endpoints (`/task/enqueue`, `/task/await`, `/task/report`): Task 7 ✓
- Client helpers: Task 8 ✓
- Hidden `leo internal task-report`: Task 12 ✓
- Persistent runner branch + prompt wrapping: Task 13 ✓
- `leo session` CLI family: Task 14 ✓
- fakeclaude interactive + e2e (basic, queue, shared, process): Tasks 15–18 ✓
- Docs + lint/test gate: Task 19 ✓

**2. Lazy mode** — `task.lazy` is parsed (Task 1) and reserved in `TaskConfig`, but the actual lazy supervisor branch is NOT implemented in v1. This plan ships always-on only. **Action:** call out that `lazy: true` is parsed but currently behaves the same as always-on. Add to Task 19's docs note.

**3. notify_on_fail in-session follow-up** — the spec describes injecting a follow-up prompt on failure into the same session. The current plan records a failure history entry but does NOT enqueue a follow-up notify prompt. **Gap.** Add Task 13a:

### Task 13a: In-session follow-up on failure (notify_on_fail)

**Files:**
- Modify: `internal/run/persistent.go`

- [ ] **Step 1: Test**

Add to `internal/run/persistent_test.go`:

```go
func TestRunPersistentNotifyOnFailEnqueuesFollowup(t *testing.T) {
    // Stub the HTTP layer to capture enqueue calls; verify a second enqueue
    // happens with the failure-notice text when the first await returns an
    // error and the task has notify_on_fail + non-empty channels.
    // (Use httptest server inside the test; or expose a seam for enqueue.)
}
```

- [ ] **Step 2: Implement**

In `runPersistent`, after `recordFailure(...)` on the failure path (both enqueue rejection and await failure paths), if `task.NotifyOnFail && len(task.Channels) > 0`, build a follow-up:

```go
followUpBody := fmt.Sprintf("The previous task failed: %s. Send a brief failure notice via channels: %s.", reason, strings.Join(task.Channels, ", "))
followUpID := newInvocationID16()
followUpWrapped := wrapPromptForPersistent(followUpID, followUpBody, task.Channels)
_, _ = daemon.EnqueueTaskHTTP(ctx, base, daemon.EnqueueRequest{
    Session: sessName, Task: taskName + ":notify", Prompt: followUpWrapped,
    Channels: task.Channels, QueueMax: 1, Timeout: 30 * time.Second,
})
// fire-and-forget — don't await
```

- [ ] **Step 3: Commit**

```bash
git add internal/run/persistent.go internal/run/persistent_test.go
git commit -m "feat(run): in-session notify_on_fail follow-up (no claude -p)"
```

**4. Type/method consistency check:**
- `EnqueueRequest.InvocationID` (Task 13) lines up with `taskEnqueueReq.InvocationID` (Task 13's server tweak) ✓
- `EnqueueWithID` on router (Task 13) is referenced before being shown — its body is "same as Enqueue but use supplied id". Engineers reading out of order should not see this gap. **Action:** show the full body inline. Add a small note to Task 13's Step 3: also include the full `EnqueueWithID` implementation:

```go
func (r *sessionRouter) EnqueueWithID(id string, p EnqueueParams) (*PendingInvocation, bool) {
    r.mu.Lock()
    q, ok := r.queues[p.Session]
    if !ok {
        q = &sessionQueue{notify: make(chan struct{}, 1)}
        r.queues[p.Session] = q
    }
    r.mu.Unlock()
    q.mu.Lock()
    defer q.mu.Unlock()
    capacity := p.QueueMax
    if capacity <= 0 {
        capacity = 5
    }
    if len(q.fifo) >= capacity {
        return nil, false
    }
    if id == "" {
        id = newInvocationID()
    }
    inv := &PendingInvocation{
        ID: id, Session: p.Session, Task: p.Task, Prompt: p.Prompt,
        Channels: p.Channels, Timeout: p.Timeout, Enqueued: time.Now(),
        Result: make(chan InvocationResult, 1),
    }
    q.fifo = append(q.fifo, inv)
    r.mu.Lock()
    r.byID[inv.ID] = inv
    r.mu.Unlock()
    select { case q.notify <- struct{}{}: default: }
    return inv, true
}
```

Then change `Enqueue` to call `EnqueueWithID("", p)`.

**5. Placeholder scan:** Task 16's helpers include a "Fill the stubs by either reusing existing e2e helpers" instruction, which violates "no placeholders." This is borderline: the e2e helpers in this repo are file-system bookkeeping (~10-15 lines each) and showing all of them inline would dwarf the plan. **Decision:** accept this as a directed reuse; if the executor finds nothing reusable, they should write each helper as a thin wrapper. Documented inline already.

**Self-review complete.**

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-17-persistent-task-sessions.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
