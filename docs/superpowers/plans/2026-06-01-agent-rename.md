# Agent Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the ability to rename a spawned ephemeral agent — a true identity rename — from the CLI and web UI, renaming a *running* agent with zero process restart.

**Architecture:** Bottom-up. A lock-guarded `procIdentity` handle becomes the single source of mutable per-process identity (name + claude args); the supervisor's `superviseProcess` watcher reads the live name from it each 5s poll, so a `tmux rename-session` is absorbed without tripping a restart. `Supervisor.RenameAgent` re-keys the in-memory maps; `agent.Manager.Rename` orchestrates supervisor + `agentstore` re-key + `--name` flag rewrite; daemon endpoint, CLI subcommand, and web handlers expose it.

**Tech Stack:** Go, cobra (CLI), net/http on a Unix socket (daemon IPC), htmx + html/template (web), tmux (session backend), `go test -race`.

**Spec:** `docs/superpowers/specs/2026-06-01-agent-rename-design.md`

---

## File Structure

- **Create** `internal/agent/name.go` — `NormalizeAgentName`.
- **Create** `internal/agent/name_test.go`.
- **Modify** `internal/agentstore/store.go` — add `Rename`.
- **Create** `internal/agentstore/rename_test.go`.
- **Create** `internal/service/identity.go` — `procIdentity` + tmux seams.
- **Create** `internal/service/identity_test.go`.
- **Modify** `internal/service/process.go` — `Supervisor.identities` map, construct/pass identity, rewrite `superviseProcess` + `waitForSessionEnd` to use it, add `Supervisor.RenameAgent`.
- **Create** `internal/service/rename_test.go` — `RenameAgent`.
- **Modify** `internal/agent/manager.go` — add `RenameAgent` to `Supervisor` interface, add `Manager.Rename`.
- **Create** `internal/agent/rename_test.go` — `Manager.Rename` (fake Supervisor).
- **Modify** `internal/daemon/server.go` — `Rename` on `AgentManager` iface + route.
- **Modify** `internal/daemon/handlers_agents.go` — `handleAgentRename`.
- **Modify** `internal/daemon/client_agents.go` — `AgentRename` client + request type.
- **Create** `internal/daemon/handlers_agents_rename_test.go`.
- **Modify** `internal/cli/agent.go` — `newAgentRenameCmd` + register.
- **Modify** `internal/web/handlers_agents.go` — `handleAPIAgentRename` + `handleWebAgentRename` + routes.
- **Modify** `internal/web/templates/partials/agents.html` — inline rename affordance.

---

## Task 1: `agentstore.Rename`

**Files:**
- Modify: `internal/agentstore/store.go`
- Test: `internal/agentstore/rename_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agentstore/rename_test.go`:

```go
package agentstore

import (
	"path/filepath"
	"testing"
)

func writeRec(t *testing.T, home string, rec Record) {
	t.Helper()
	if err := Save(home, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestRename_ReKeysAndMutates(t *testing.T) {
	home := t.TempDir()
	writeRec(t, home, Record{Name: "leo-old", Template: "t", ClaudeArgs: []string{"--name", "leo-old", "--model", "opus"}})

	err := Rename(home, "leo-old", "leo-new", func(r Record) Record {
		r.Name = "leo-new"
		r.ClaudeArgs = []string{"--name", "leo-new", "--model", "opus"}
		return r
	})
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	recs, err := Load(filepath.Join(home, "state", "agents.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := recs["leo-old"]; ok {
		t.Fatal("old key still present")
	}
	got, ok := recs["leo-new"]
	if !ok {
		t.Fatal("new key missing")
	}
	if got.Name != "leo-new" || got.ClaudeArgs[1] != "leo-new" {
		t.Fatalf("mutate not applied: %+v", got)
	}
}

func TestRename_CollisionAndMissing(t *testing.T) {
	home := t.TempDir()
	writeRec(t, home, Record{Name: "leo-a"})
	writeRec(t, home, Record{Name: "leo-b"})

	if err := Rename(home, "leo-a", "leo-b", func(r Record) Record { return r }); err == nil {
		t.Fatal("expected collision error")
	}
	if err := Rename(home, "leo-missing", "leo-c", func(r Record) Record { return r }); err == nil {
		t.Fatal("expected missing-source error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentstore/ -run TestRename -v`
Expected: FAIL — `undefined: Rename`.

- [ ] **Step 3: Implement `Rename`**

Append to `internal/agentstore/store.go` (after `Remove`):

```go
// Rename atomically re-keys an agent record from old to new, applying mutate to
// the record before it is stored under the new key. It errors if old is absent
// or new already exists. The whole load-modify-write happens under storeMu so it
// is consistent with concurrent Save/Remove/Load.
func Rename(homePath, old, new string, mutate func(Record) Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	rec, ok := records[old]
	if !ok {
		return fmt.Errorf("agent %q not found", old)
	}
	if _, exists := records[new]; exists {
		return fmt.Errorf("agent %q already exists", new)
	}
	records[new] = mutate(rec)
	delete(records, old)
	return write(path, records)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agentstore/ -run TestRename -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentstore/store.go internal/agentstore/rename_test.go
git commit -m "feat(agentstore): add atomic Rename with mutate callback"
```

---

## Task 2: `NormalizeAgentName`

**Files:**
- Create: `internal/agent/name.go`
- Test: `internal/agent/name_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/name_test.go`:

```go
package agent

import "testing"

func TestNormalizeAgentName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"auth-refactor", "leo-auth-refactor", false},
		{"  Auth_Refactor  ", "", true}, // underscore is not allowed
		{"leo-already-prefixed", "leo-already-prefixed", false},
		{"UPPER", "leo-upper", false},
		{"with spaces", "", true},
		{"dots.bad", "", true},
		{"colon:bad", "", true},
		{"slash/bad", "", true},
		{"", "", true},
		{"leo-", "", true}, // empty after prefix
		{"--leading", "leo-leading", false},
		{"trailing--", "leo-trailing", false},
	}
	for _, c := range cases {
		got, err := NormalizeAgentName(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeAgentName(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAgentName(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeAgentName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAgentName_LengthCap(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	got, err := NormalizeAgentName(string(long))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > 64 {
		t.Fatalf("name not capped: len=%d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestNormalizeAgentName -v`
Expected: FAIL — `undefined: NormalizeAgentName`.

- [ ] **Step 3: Implement `NormalizeAgentName`**

Create `internal/agent/name.go`:

```go
package agent

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	agentNamePrefix    = "leo-"
	maxAgentNameLength = 64
)

// charsetRe rejects anything outside lowercase alphanumerics and dashes. It is
// applied to the body after the leo- prefix is stripped, so the user's input
// must be tmux-safe and slug-shaped (no dots, colons, slashes, spaces, etc.).
var charsetRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// NormalizeAgentName validates and canonicalizes a user-supplied agent name.
// It lowercases, ensures exactly one leo- prefix, rejects tmux-hostile
// characters, collapses repeated/edge dashes, and caps the total length so the
// stored name always equals the tmux session name (agent.SessionName).
func NormalizeAgentName(raw string) (string, error) {
	body := strings.ToLower(strings.TrimSpace(raw))
	body = strings.TrimPrefix(body, agentNamePrefix)
	if body == "" {
		return "", fmt.Errorf("agent name is empty")
	}
	if !charsetRe.MatchString(body) {
		return "", fmt.Errorf("agent name %q has invalid characters (allowed: a-z, 0-9, dash)", raw)
	}
	// Collapse runs of dashes and trim leading/trailing dashes.
	for strings.Contains(body, "--") {
		body = strings.ReplaceAll(body, "--", "-")
	}
	body = strings.Trim(body, "-")
	if body == "" {
		return "", fmt.Errorf("agent name %q reduces to empty after normalization", raw)
	}
	name := agentNamePrefix + body
	if len(name) > maxAgentNameLength {
		name = name[:maxAgentNameLength]
		name = strings.TrimRight(name, "-")
	}
	return name, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestNormalizeAgentName -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/name.go internal/agent/name_test.go
git commit -m "feat(agent): add NormalizeAgentName validator"
```

---

## Task 3: `procIdentity` handle + tmux seams

**Files:**
- Create: `internal/service/identity.go`
- Test: `internal/service/identity_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/service/identity_test.go`:

```go
package service

import (
	"sync"
	"testing"
)

func TestProcIdentity_RenameRewritesNameArg(t *testing.T) {
	id := newProcIdentity("leo-old", []string{"--name", "leo-old", "--model", "opus"})

	if id.Name() != "leo-old" {
		t.Fatalf("Name = %q", id.Name())
	}
	if id.SessionName() != "leo-old" {
		t.Fatalf("SessionName = %q", id.SessionName())
	}

	id.rename("leo-new")
	if id.Name() != "leo-new" {
		t.Fatalf("after rename Name = %q", id.Name())
	}
	args := id.Args()
	if args[1] != "leo-new" {
		t.Fatalf("--name not rewritten: %v", args)
	}
	// Args returns a copy: mutating it must not affect the handle.
	args[1] = "tampered"
	if id.Args()[1] != "leo-new" {
		t.Fatal("Args did not return a copy")
	}
}

func TestProcIdentity_ConcurrentAccess(t *testing.T) {
	id := newProcIdentity("leo-a", []string{"--name", "leo-a"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = id.Name(); _ = id.Args() }()
		go func() { defer wg.Done(); id.rename("leo-b") }()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/ -run TestProcIdentity -v`
Expected: FAIL — `undefined: newProcIdentity`.

- [ ] **Step 3: Implement `procIdentity` + seams**

Create `internal/service/identity.go`:

```go
package service

import (
	"os/exec"
	"sync"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// procIdentity is the single source of truth for a supervised process's mutable
// identity: its name (which drives the tmux session name and supervisor map
// keys) and its claude args (which carry --name). superviseProcess reads from
// it on every poll/iteration so a live RenameAgent is picked up without a
// process restart.
type procIdentity struct {
	mu   sync.RWMutex
	name string
	args []string
}

func newProcIdentity(name string, args []string) *procIdentity {
	cp := make([]string, len(args))
	copy(cp, args)
	return &procIdentity{name: name, args: cp}
}

func (p *procIdentity) Name() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

// SessionName returns the tmux session name for the current identity name.
func (p *procIdentity) SessionName() string {
	return agent.SessionName(p.Name())
}

// Args returns a copy of the current claude args.
func (p *procIdentity) Args() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]string, len(p.args))
	copy(cp, p.args)
	return cp
}

// setArgs replaces the stored args (used by the quick-exit strip-resume path).
func (p *procIdentity) setArgs(args []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(args))
	copy(cp, args)
	p.args = cp
}

// rename swaps the name and rewrites the value following --name in args. The
// caller (RenameAgent) holds this under the supervisor lock so the tmux session
// rename and this swap are observed atomically by the watcher's RLock.
func (p *procIdentity) rename(newName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name = newName
	for i := 0; i+1 < len(p.args); i++ {
		if p.args[i] == "--name" {
			p.args[i+1] = newName
			break
		}
	}
}

// tmuxRenameSession and tmuxHasSession are package-level seams so RenameAgent
// and waitForSessionEnd can be unit-tested without a real tmux. They default to
// real exec, mirroring supervisedExecFn.
var tmuxRenameSession = func(tmuxPath, old, new string) error {
	return exec.Command(tmuxPath, tmux.Args("rename-session", "-t", old, new)...).Run()
}

var tmuxHasSession = func(tmuxPath, session string) bool {
	return exec.Command(tmuxPath, tmux.Args("has-session", "-t", session)...).Run() == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/service/ -run TestProcIdentity -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/identity.go internal/service/identity_test.go
git commit -m "feat(service): add procIdentity handle and tmux seams"
```

---

## Task 4: Thread identity through `superviseProcess` + `Supervisor.RenameAgent`

**Files:**
- Modify: `internal/service/process.go`
- Test: `internal/service/rename_test.go`

This task changes the supervisor's hot loop. Make the edits, keep the build green, then add `RenameAgent` with its test.

- [ ] **Step 1: Add the `identities` map to the `Supervisor` struct**

In `internal/service/process.go`, in the `Supervisor` struct (after `reservations`):

```go
	reservations map[string]struct{}           // names atomically claimed by ReserveAgent before SpawnAgent
	identities   map[string]*procIdentity      // live identity handle per ephemeral agent, re-keyed on rename
```

In `NewSupervisor`, add to the returned literal:

```go
		reservations: make(map[string]struct{}),
		identities:   make(map[string]*procIdentity),
		ctx:          ctx,
```

- [ ] **Step 2: Construct + store the identity in `SpawnAgent`, pass it to the goroutine**

In `SpawnAgent`, replace the state-registration + goroutine launch block. Find:

```go
	childCtx, cancel := context.WithCancel(s.ctx) // #nosec G118 -- cancel stored in s.cancels, called by StopAgent
	s.cancels[spec.Name] = cancel
	s.states[spec.Name] = &ProcessState{
		Name:      spec.Name,
		Status:    "starting",
		StartedAt: time.Now(),
		Ephemeral: true,
	}
	s.mu.Unlock()

	procSpec := ProcessSpec{
		Name:       spec.Name,
		ClaudeArgs: spec.ClaudeArgs,
		WorkDir:    spec.WorkDir,
		Env:        spec.Env,
		WebPort:    spec.WebPort,
		WebToken:   spec.WebToken,
	}
	go superviseProcess(childCtx, s.tmuxPath, s.claudePath, procSpec, s.homePath, s)
	return nil
```

Replace with:

```go
	childCtx, cancel := context.WithCancel(s.ctx) // #nosec G118 -- cancel stored in s.cancels, called by StopAgent
	s.cancels[spec.Name] = cancel
	s.states[spec.Name] = &ProcessState{
		Name:      spec.Name,
		Status:    "starting",
		StartedAt: time.Now(),
		Ephemeral: true,
	}
	id := newProcIdentity(spec.Name, spec.ClaudeArgs)
	s.identities[spec.Name] = id
	s.mu.Unlock()

	procSpec := ProcessSpec{
		Name:       spec.Name,
		ClaudeArgs: spec.ClaudeArgs,
		WorkDir:    spec.WorkDir,
		Env:        spec.Env,
		WebPort:    spec.WebPort,
		WebToken:   spec.WebToken,
	}
	go superviseProcess(childCtx, s.tmuxPath, s.claudePath, procSpec, s.homePath, s, id)
	return nil
```

- [ ] **Step 3: Clean up the identity in `StopAgent`**

In `StopAgent`, find the cleanup block:

```go
	s.mu.Lock()
	delete(s.states, name)
	delete(s.cancels, name)
	s.mu.Unlock()
```

Replace with:

```go
	s.mu.Lock()
	delete(s.states, name)
	delete(s.cancels, name)
	delete(s.identities, name)
	s.mu.Unlock()
```

- [ ] **Step 4: Pass an identity at the config-process call site**

In `defaultSupervisedExec` (around line 470), find:

```go
			superviseProcess(ctx, tmuxPath, claudePath, spec, homePath, supervisor)
```

Replace with (config processes are never renamed, so the identity is local and not stored in `supervisor.identities`):

```go
			superviseProcess(ctx, tmuxPath, claudePath, spec, homePath, supervisor, newProcIdentity(spec.Name, spec.ClaudeArgs))
```

- [ ] **Step 5: Rewrite `superviseProcess` to use the identity**

Replace the entire `superviseProcess` function with:

```go
func superviseProcess(ctx context.Context, tmuxPath, claudePath string, spec ProcessSpec, homePath string, sv *Supervisor, id *procIdentity) {
	sv.initState(spec.Name)

	backoff := initialBackoff

	for {
		// Snapshot identity for this iteration. The tmux session name is also
		// re-read live by waitForSessionEnd (via id) so a rename mid-wait is
		// absorbed; the snapshot here governs this iteration's kill/new-session
		// and name-keyed state files.
		name := id.Name()
		sessionName := id.SessionName()
		currentArgs := id.Args()

		sv.setState(name, "running")

		if spec.StateDir != "" {
			resetExitCode(spec.StateDir, name)
		}

		claudeCmd := buildClaudeShellCmd(claudePath, currentArgs, tmuxPath, spec, os.Getenv("PATH"), os.Stderr)

		exec.Command(tmuxPath, tmux.Args("kill-session", "-t", sessionName)...).Run()

		createCmd := exec.CommandContext(ctx, tmuxPath,
			tmux.Args(
				"new-session", "-d", "-s", sessionName,
				"-c", spec.WorkDir,
				"-x", "200", "-y", "50",
				claudeCmd,
			)...,
		)
		createCmd.Dir = spec.WorkDir
		createCmd.Env = os.Environ()

		startTime := time.Now()

		if err := createCmd.Run(); err != nil {
			sv.setState(name, "restarting")
			fmt.Fprintf(os.Stderr, "[%s] tmux new-session failed: %v, retrying in %s\n", name, err, backoff)
			select {
			case <-ctx.Done():
				sv.setState(name, "stopped")
				return
			case <-time.After(backoff):
			}
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
			continue
		}

		fmt.Fprintf(os.Stdout, "[%s] tmux session '%s' created, claude running\n", name, sessionName)

		if hasDevChannelFlag(currentArgs) {
			fmt.Fprintf(os.Stdout, "[%s] auto-accepting dev-channel prompt\n", name)
			go func(sess string) {
				if err := tmux.AcceptDevChannelPrompt(ctx, tmuxPath, sess); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "[%s] warning: dev-channel auto-accept failed: %v\n", name, err)
				}
			}(sessionName)
		}

		if waitForSessionEnd(ctx, tmuxPath, id, spec, startTime) {
			sv.setState(name, "stopped")
			return
		}

		elapsed := time.Since(startTime)

		select {
		case <-ctx.Done():
			sv.setState(name, "stopped")
			return
		default:
		}

		sv.setState(name, "restarting")
		sv.incrementRestarts(name)

		if elapsed < quickExitThreshold {
			hadResume := hasResumeArg(currentArgs)
			currentArgs = stripResumeArg(currentArgs)
			id.setArgs(currentArgs)
			clearProcessSession(homePath, name)
			if hadResume {
				markAgentNoResume(homePath, name)
			}
			fmt.Fprintf(os.Stderr, "[%s] claude exited quickly (%.0fs), cleared stale session\n", name, elapsed.Seconds())
		}

		exitCode, codeOK := 0, false
		signal := "none"
		var tail []string
		if spec.StateDir != "" {
			exitCode, codeOK = readExitCode(spec.StateDir, name)
			if codeOK {
				signal = decodeSignal(exitCode)
			}
			tail = tailLines(processStderrPath(spec.StateDir, name), exitStderrTailLines)
			_ = writeExitLog(spec.StateDir, name, exitCode, codeOK, signal, elapsed, tail)
		}
		logProcessExit(os.Stderr, name, elapsed, backoff, exitCode, codeOK, signal,
			processExitLogPath(spec.StateDir, name), len(tail) > 0)

		select {
		case <-ctx.Done():
			sv.setState(name, "stopped")
			return
		case <-time.After(backoff):
		}
		backoff = advanceBackoff(backoff, elapsed)
	}
}
```

- [ ] **Step 6: Rewrite `waitForSessionEnd` to read the live session name**

Replace the entire `waitForSessionEnd` function with:

```go
func waitForSessionEnd(ctx context.Context, tmuxPath string, id *procIdentity, spec ProcessSpec, startTime time.Time) bool {
	_ = startTime // kept in signature for future lifecycle hooks
	for {
		select {
		case <-ctx.Done():
			exec.Command(tmuxPath, tmux.Args("kill-session", "-t", id.SessionName())...).Run()
			return true
		case <-time.After(5 * time.Second):
		}

		// Re-read the session name each poll so a live rename is followed
		// rather than reported as a vanished session.
		sessionName := id.SessionName()
		if !tmuxHasSession(tmuxPath, sessionName) {
			return false
		}

		autoResumePrompt(tmuxPath, sessionName, id.Name())
	}
}
```

- [ ] **Step 7: Build to verify the refactor compiles**

Run: `go build ./...`
Expected: success (no errors). If `autoResumePrompt`'s third arg differs, match its actual signature — it previously received `spec.Name`; pass `id.Name()`.

- [ ] **Step 8: Write the failing `RenameAgent` test**

Create `internal/service/rename_test.go`:

```go
package service

import (
	"context"
	"testing"
)

// newTestSupervisor builds a supervisor with a single fake-running ephemeral
// agent, bypassing the real spawn goroutine. tmux seams are stubbed.
func newTestSupervisor(t *testing.T, name string) *Supervisor {
	t.Helper()
	s := NewSupervisor(context.Background())
	s.states[name] = &ProcessState{Name: name, Status: "running", Ephemeral: true}
	s.cancels[name] = func() {}
	s.identities[name] = newProcIdentity(name, []string{"--name", name})
	return s
}

func TestRenameAgent_ReKeysMaps(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return nil }
	defer func() { tmuxRenameSession = origRename }()

	s := newTestSupervisor(t, "leo-old")
	if err := s.RenameAgent("leo-old", "leo-new"); err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}

	if _, ok := s.states["leo-old"]; ok {
		t.Fatal("old state key still present")
	}
	st, ok := s.states["leo-new"]
	if !ok || st.Name != "leo-new" {
		t.Fatalf("new state missing/mislabeled: %+v", st)
	}
	if _, ok := s.cancels["leo-new"]; !ok {
		t.Fatal("cancel not re-keyed")
	}
	id, ok := s.identities["leo-new"]
	if !ok || id.Name() != "leo-new" || id.Args()[1] != "leo-new" {
		t.Fatalf("identity not re-keyed/rewritten: %+v", id)
	}
}

func TestRenameAgent_Rejections(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return nil }
	defer func() { tmuxRenameSession = origRename }()

	// Collision with existing state.
	s := newTestSupervisor(t, "leo-old")
	s.states["leo-taken"] = &ProcessState{Name: "leo-taken", Status: "running", Ephemeral: true}
	if err := s.RenameAgent("leo-old", "leo-taken"); err == nil {
		t.Fatal("expected collision error")
	}

	// Non-running agent is rejected (retryable).
	s2 := newTestSupervisor(t, "leo-x")
	s2.states["leo-x"].Status = "restarting"
	if err := s2.RenameAgent("leo-x", "leo-y"); err == nil {
		t.Fatal("expected non-running rejection")
	}

	// Non-ephemeral (config) process is rejected.
	s3 := NewSupervisor(context.Background())
	s3.states["proc"] = &ProcessState{Name: "proc", Status: "running"}
	if err := s3.RenameAgent("proc", "leo-z"); err == nil {
		t.Fatal("expected non-ephemeral rejection")
	}

	// Missing source is rejected.
	s4 := NewSupervisor(context.Background())
	if err := s4.RenameAgent("leo-missing", "leo-q"); err == nil {
		t.Fatal("expected missing-source rejection")
	}
}

func TestRenameAgent_TmuxFailureLeavesStateUntouched(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return context.DeadlineExceeded }
	defer func() { tmuxRenameSession = origRename }()

	s := newTestSupervisor(t, "leo-old")
	if err := s.RenameAgent("leo-old", "leo-new"); err == nil {
		t.Fatal("expected tmux rename failure to propagate")
	}
	if _, ok := s.states["leo-old"]; !ok {
		t.Fatal("old state was removed despite tmux failure")
	}
	if _, ok := s.states["leo-new"]; ok {
		t.Fatal("new state created despite tmux failure")
	}
}
```

- [ ] **Step 9: Run to verify it fails**

Run: `go test ./internal/service/ -run TestRenameAgent -v`
Expected: FAIL — `s.RenameAgent undefined`.

- [ ] **Step 10: Implement `Supervisor.RenameAgent`**

Append to `internal/service/process.go` (after `StopAgent`):

```go
// RenameAgent renames a running ephemeral agent with zero process restart. It
// renames the tmux session, swaps the live identity handle (so the supervise
// goroutine follows the new name), and re-keys the states/cancels/identities
// maps. A non-running agent (mid-restart) returns a retryable error so callers
// do not race the goroutine's create window.
func (s *Supervisor) RenameAgent(old, new string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.states[old]
	if !ok {
		return fmt.Errorf("agent %q not found", old)
	}
	if !st.Ephemeral {
		return fmt.Errorf("%q is not an ephemeral agent", old)
	}
	if st.Status != "running" {
		return fmt.Errorf("agent %q is %s, not running; retry once it settles", old, st.Status)
	}
	if _, exists := s.states[new]; exists {
		return fmt.Errorf("agent %q already exists", new)
	}
	if _, reserved := s.reservations[new]; reserved {
		return fmt.Errorf("agent %q is reserved", new)
	}
	id, ok := s.identities[old]
	if !ok {
		return fmt.Errorf("agent %q has no identity handle", old)
	}

	// Hold the identity write-lock across the tmux rename + name swap so the
	// watcher's RLock observes either (old,old) or (new,new), never a crossed
	// state. tmux rename-session keeps the running pane alive.
	id.mu.Lock()
	if err := tmuxRenameSession(s.tmuxPath, agent.SessionName(old), agent.SessionName(new)); err != nil {
		id.mu.Unlock()
		return fmt.Errorf("renaming tmux session: %w", err)
	}
	id.name = new
	for i := 0; i+1 < len(id.args); i++ {
		if id.args[i] == "--name" {
			id.args[i+1] = new
			break
		}
	}
	id.mu.Unlock()

	st.Name = new
	s.states[new] = st
	s.cancels[new] = s.cancels[old]
	s.identities[new] = id
	delete(s.states, old)
	delete(s.cancels, old)
	delete(s.identities, old)
	return nil
}
```

Note: `agent` is already imported in `process.go` (used by `StopAgent`'s `agent.SessionName`).

- [ ] **Step 11: Run tests to verify they pass**

Run: `go test ./internal/service/ -run 'TestRenameAgent|TestProcIdentity' -race -v`
Expected: PASS.

- [ ] **Step 12: Run the full service package to catch regressions from the refactor**

Run: `go test ./internal/service/ -race`
Expected: PASS (existing supervisor tests still green).

- [ ] **Step 13: Commit**

```bash
git add internal/service/process.go internal/service/rename_test.go
git commit -m "feat(service): zero-restart RenameAgent via live identity handle"
```

---

## Task 5: `agent.Manager.Rename`

**Files:**
- Modify: `internal/agent/manager.go`
- Test: `internal/agent/rename_test.go`

- [ ] **Step 1: Add `RenameAgent` to the `Supervisor` interface**

In `internal/agent/manager.go`, in the `Supervisor` interface, add the method:

```go
type Supervisor interface {
	ReserveAgent(name string) error
	ReleaseAgent(name string)
	SpawnAgent(spec SpawnRequest) error
	StopAgent(name string) error
	RenameAgent(old, new string) error
	EphemeralAgents() map[string]ProcessState
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/agent/rename_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// fakeSupervisor records RenameAgent calls and reports liveness via ephemeral.
type fakeSupervisor struct {
	ephemeral  map[string]ProcessState
	renamedOld string
	renamedNew string
	renameErr  error
}

func (f *fakeSupervisor) ReserveAgent(string) error          { return nil }
func (f *fakeSupervisor) ReleaseAgent(string)                {}
func (f *fakeSupervisor) SpawnAgent(SpawnRequest) error      { return nil }
func (f *fakeSupervisor) StopAgent(string) error             { return nil }
func (f *fakeSupervisor) EphemeralAgents() map[string]ProcessState {
	return f.ephemeral
}
func (f *fakeSupervisor) RenameAgent(old, new string) error {
	f.renamedOld, f.renamedNew = old, new
	return f.renameErr
}

func newTestManager(t *testing.T, home string, sup Supervisor) *Manager {
	t.Helper()
	loader := func() (*config.Config, error) {
		return &config.Config{HomePath: home}, nil
	}
	return New(loader, sup, "", "")
}

func TestManagerRename_RunningAgent(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-old",
		ClaudeArgs: []string{"--name", "leo-old", "--model", "opus"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	m := newTestManager(t, home, sup)

	rec, err := m.Rename("leo-old", "renamed-agent")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if rec.Name != "leo-renamed-agent" {
		t.Fatalf("returned record name = %q", rec.Name)
	}
	if sup.renamedOld != "leo-old" || sup.renamedNew != "leo-renamed-agent" {
		t.Fatalf("supervisor not called: %q -> %q", sup.renamedOld, sup.renamedNew)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := recs["leo-renamed-agent"]
	if !ok {
		t.Fatal("store not re-keyed")
	}
	if strings.Join(got.ClaudeArgs, " ") != "--name leo-renamed-agent --model opus" {
		t.Fatalf("--name not rewritten: %v", got.ClaudeArgs)
	}
}

func TestManagerRename_StoppedAgentSkipsSupervisor(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-stopped",
		Branch:     "feature",
		Stopped:    true,
		ClaudeArgs: []string{"--name", "leo-stopped"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}} // not live
	m := newTestManager(t, home, sup)

	if _, err := m.Rename("leo-stopped", "leo-revived"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if sup.renamedNew != "" {
		t.Fatal("supervisor RenameAgent should not be called for a stopped agent")
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["leo-revived"]; !ok {
		t.Fatal("store not re-keyed for stopped agent")
	}
}

func TestManagerRename_Errors(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", ClaudeArgs: []string{"--name", "leo-a"}})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-b", ClaudeArgs: []string{"--name", "leo-b"}})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
	m := newTestManager(t, home, sup)

	// Collision.
	if _, err := m.Rename("leo-a", "leo-b"); err == nil {
		t.Fatal("expected collision error")
	}
	// Unchanged name.
	if _, err := m.Rename("leo-a", "leo-a"); err == nil {
		t.Fatal("expected unchanged-name error")
	}
	// Invalid name.
	if _, err := m.Rename("leo-a", "bad name!"); err == nil {
		t.Fatal("expected invalid-name error")
	}
}
```

If `config` is not yet imported in the test, the package already imports it elsewhere; add `"github.com/blackpaw-studio/leo/internal/config"` to this test file's imports.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/agent/ -run TestManagerRename -v`
Expected: FAIL — `m.Rename undefined`.

- [ ] **Step 4: Implement `Manager.Rename`**

Append to `internal/agent/manager.go`:

```go
// Rename changes an agent's identity. The agent is fuzzy-resolved from query,
// the new name is normalized and checked for collisions, the live supervisor
// state is renamed in place (zero restart) when the agent is running, and the
// persisted record is re-keyed with its --name flag rewritten. Stopped worktree
// agents skip the supervisor and only re-key the store.
func (m *Manager) Rename(query, rawNewName string) (Record, error) {
	rec, err := m.Resolve(query)
	if err != nil {
		return Record{}, err
	}
	oldName := rec.Name

	newName, err := NormalizeAgentName(rawNewName)
	if err != nil {
		return Record{}, err
	}
	if newName == oldName {
		return Record{}, fmt.Errorf("agent is already named %q", oldName)
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}

	if _, live := m.sup.EphemeralAgents()[oldName]; live {
		if err := m.sup.RenameAgent(oldName, newName); err != nil {
			return Record{}, fmt.Errorf("renaming running agent: %w", err)
		}
	}

	if err := agentstore.Rename(cfg.HomePath, oldName, newName, func(r agentstore.Record) agentstore.Record {
		r.Name = newName
		r.ClaudeArgs = rewriteNameArg(r.ClaudeArgs, newName)
		return r
	}); err != nil {
		return Record{}, fmt.Errorf("persisting rename: %w", err)
	}

	rec.Name = newName
	return rec, nil
}

// rewriteNameArg returns a copy of args with the value following --name replaced
// by newName. If --name is absent the args are returned unchanged.
func rewriteNameArg(args []string, newName string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--name" {
			out[i+1] = newName
			break
		}
	}
	return out
}
```

Note: `Resolve` only matches live agents; for a stopped worktree agent whose record exists but is not live, confirm `Resolve` still finds it. If it does not, the test `TestManagerRename_StoppedAgentSkipsSupervisor` will fail at the resolve step — in that case, extend `Rename` to fall back to an agentstore lookup by exact name when `Resolve` returns not-found. Implement that fallback now:

```go
// after the failed m.Resolve, before returning, in Rename:
//   rec, err := m.Resolve(query)
//   if err != nil {
//       if exact, ok := lookupStoredAgent(cfg.HomePath, query); ok {
//           rec = exact
//       } else {
//           return Record{}, err
//       }
//   }
```

Only add the fallback if Step 5 shows `Resolve` cannot find stopped agents. Keep the implementation minimal — prefer the simple version first and let the test tell you.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestManagerRename -race -v`
Expected: PASS. If the stopped-agent test fails on resolve, add the agentstore fallback described above (a small helper that loads the store and returns the record matching the exact/normalized query as an `agent.Record`), then re-run.

- [ ] **Step 6: Run the full agent package**

Run: `go test ./internal/agent/ -race`
Expected: PASS (the new `RenameAgent` interface method may require updating any other in-package fake `Supervisor` implementations in existing tests — add a no-op `RenameAgent` to them if the build fails).

- [ ] **Step 7: Commit**

```bash
git add internal/agent/manager.go internal/agent/rename_test.go
git commit -m "feat(agent): Manager.Rename orchestrating supervisor + store re-key"
```

---

## Task 6: Daemon endpoint + client

**Files:**
- Modify: `internal/daemon/server.go`, `internal/daemon/handlers_agents.go`, `internal/daemon/client_agents.go`
- Test: `internal/daemon/handlers_agents_rename_test.go`

- [ ] **Step 1: Add `Rename` to the `AgentManager` interface + register the route**

In `internal/daemon/server.go`, add to the `AgentManager` interface:

```go
	Resolve(query string) (agent.Record, error)
	Rename(query, newName string) (agent.Record, error)
```

In the route block (near the other `/agents/...` routes):

```go
	mux.HandleFunc("POST /agents/{name}/rename", s.handleAgentRename)
```

- [ ] **Step 2: Write the failing handler test**

Create `internal/daemon/handlers_agents_rename_test.go`. Mirror the existing agent-handler tests in this package for the fake manager shape (check `handlers_agents_test.go` for the established fake and `newTestServer` helper, and reuse them):

```go
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

type renameFakeMgr struct {
	AgentManager // embed to satisfy unused methods; only Resolve+Rename used
	resolveRec   agent.Record
	gotQuery     string
	gotNew       string
}

func (f *renameFakeMgr) Resolve(q string) (agent.Record, error) {
	return f.resolveRec, nil
}
func (f *renameFakeMgr) Rename(query, newName string) (agent.Record, error) {
	f.gotQuery, f.gotNew = query, newName
	return agent.Record{Name: newName}, nil
}

func TestHandleAgentRename(t *testing.T) {
	mgr := &renameFakeMgr{resolveRec: agent.Record{Name: "leo-old"}}
	s := &Server{agentMgr: mgr}

	body, _ := json.Marshal(map[string]string{"new_name": "leo-new"})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/agents/leo-old/rename", bytes.NewReader(body))
	req.SetPathValue("name", "leo-old")
	rec := httptest.NewRecorder()

	s.handleAgentRename(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if mgr.gotQuery != "leo-old" || mgr.gotNew != "leo-new" {
		t.Fatalf("Rename called with %q -> %q", mgr.gotQuery, mgr.gotNew)
	}
}

func TestHandleAgentRename_MissingNewName(t *testing.T) {
	mgr := &renameFakeMgr{resolveRec: agent.Record{Name: "leo-old"}}
	s := &Server{agentMgr: mgr}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/agents/leo-old/rename", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("name", "leo-old")
	rec := httptest.NewRecorder()

	s.handleAgentRename(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
```

If the existing tests use a complete fake manager struct rather than interface embedding, copy that pattern instead — the embedding shortcut above works only if `AgentManager` is the interface type; adjust to match the package's conventions.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestHandleAgentRename -v`
Expected: FAIL — `s.handleAgentRename undefined`.

- [ ] **Step 4: Implement the handler**

Append to `internal/daemon/handlers_agents.go`:

```go
// handleAgentRename renames an agent. The {name} path segment may be a shorthand
// query; it is resolved to the canonical agent, then Rename applies the new name
// across supervisor state and the persisted record.
func (s *Server) handleAgentRename(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if req.NewName == "" {
		writeError(w, http.StatusBadRequest, "new_name is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	updated, err := s.agentMgr.Rename(rec.Name, req.NewName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
```

Confirm `encoding/json` and `fmt` are imported in `handlers_agents.go` (the existing `handleAgentLogs`/`handleAgentSpawn` already use them).

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/daemon/ -run TestHandleAgentRename -race -v`
Expected: PASS.

- [ ] **Step 6: Add the client function**

In `internal/daemon/client_agents.go`, mirror `AgentStop`. First read `AgentStop` and the shared request helper it uses (`doAgentRequest` or equivalent — find the exact helper with `grep -n "func AgentStop" -A 12 internal/daemon/client_agents.go`), then add:

```go
// AgentRenameRequest is the body for POST /agents/{name}/rename.
type AgentRenameRequest struct {
	NewName string `json:"new_name"`
}

// AgentRename renames the agent matching query to newName via the daemon.
// Returns the updated record.
func AgentRename(ctx context.Context, workDir, query, newName string) (agent.Record, error) {
	// Use the same request/encode/decode pattern as AgentSpawn (which returns a
	// record) targeting POST /agents/{query}/rename with an AgentRenameRequest
	// body. Reuse the package's existing socket-request helper rather than
	// hand-rolling http here.
	return agentPostRecord(ctx, workDir, "/agents/"+url.PathEscape(query)+"/rename", AgentRenameRequest{NewName: newName})
}
```

Replace `agentPostRecord` with the actual helper name used by `AgentSpawn` in this file (read `AgentSpawn` to copy its exact request mechanism — it likely builds a `*http.Request` against the Unix socket transport and JSON-decodes an `agent.Record`). Add `"net/url"` to imports if not present.

- [ ] **Step 7: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/server.go internal/daemon/handlers_agents.go internal/daemon/client_agents.go internal/daemon/handlers_agents_rename_test.go
git commit -m "feat(daemon): /agents/{name}/rename endpoint and client"
```

---

## Task 7: CLI `leo agent rename`

**Files:**
- Modify: `internal/cli/agent.go`

- [ ] **Step 1: Add the subcommand constructor**

In `internal/cli/agent.go`, add a new constructor modeled on `newAgentStopCmd` (which is the canonical pattern for resolve-then-daemon-call with remote dispatch):

```go
func newAgentRenameCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "rename <name> <new-name>",
		Short: "Rename an agent",
		Long: `Rename an agent's identity. A running agent is renamed in place with no
process restart; its claude session keeps running. The new name is normalized to
a leo- prefixed slug (lowercase, a-z 0-9 and dashes only).`,
		Example: `  leo agent rename leo-mcp-node-owner-fetch auth-refactor`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, newName := args[0], args[1]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"rename", name, newName})
			}
			updated, err := daemon.AgentRename(cmd.Context(), cfg.HomePath, name, newName)
			if err != nil {
				return fmt.Errorf("renaming agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "renamed %s -> %s\n", name, updated.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}
```

- [ ] **Step 2: Register the subcommand**

In `newAgentCmd`, add `newAgentRenameCmd()` to the `cmd.AddCommand(...)` list:

```go
	cmd.AddCommand(
		newAgentListCmd(),
		newAgentSpawnCmd(),
		newAgentAttachCmd(),
		newAgentStopCmd(),
		newAgentRenameCmd(),
		newAgentPruneCmd(),
		newAgentLogsCmd(),
		newAgentSessionNameCmd(),
	)
```

- [ ] **Step 3: Build and exercise help**

Run: `go build ./... && go run ./cmd/leo agent rename --help`
Expected: build succeeds; help prints the `rename` usage with the `--host` flag.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/agent.go
git commit -m "feat(cli): add leo agent rename subcommand"
```

---

## Task 8: Web UI rename

**Files:**
- Modify: `internal/web/handlers_agents.go`, `internal/web/templates/partials/agents.html`

- [ ] **Step 1: Read the existing stop-handler pair and routes**

Run: `grep -n "AgentStop\|agent/.*stop\|HandleFunc\|handleAPIAgent\|handleWebAgent\|partialAgents\|renderAgents" internal/web/handlers_agents.go internal/web/*.go`
Identify: (a) how `handleAPIAgentStop` and `handleWebAgentStop` resolve the agent and call the manager, (b) how routes are registered, (c) how `handleWebAgentStop` re-renders the agents partial on success. Mirror these exactly.

- [ ] **Step 2: Add the API + web handlers**

Append to `internal/web/handlers_agents.go`, matching the package's existing handler signatures, manager accessor (`s.agentMgr` / `s.agents` — use whatever the stop handler uses), and partial-render helper:

```go
// handleAPIAgentRename renames an agent via JSON: POST /api/agent/{name}/rename
// with {"new_name":"..."}. Returns the updated record.
func (s *Server) handleAPIAgentRename(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewName == "" {
		http.Error(w, "new_name is required", http.StatusBadRequest)
		return
	}
	updated, err := s.renameAgent(name, req.NewName) // thin wrapper over the agent manager; see Step 3
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, updated) // use the package's existing JSON writer
}

// handleWebAgentRename handles the inline form: POST /web/agent/{name}/rename
// with a new_name form field, then re-renders the agents partial.
func (s *Server) handleWebAgentRename(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	newName := r.FormValue("new_name")
	if newName == "" {
		http.Error(w, "new name is required", http.StatusBadRequest)
		return
	}
	if _, err := s.renameAgent(name, newName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.renderAgentsPartial(w, r) // use the exact helper handleWebAgentStop calls
}
```

`renameAgent`, `writeJSON`, and `renderAgentsPartial` are placeholders for whatever the stop handler already uses — replace them with the real accessor/helper names from Step 1 (e.g. the manager is reached the same way `handleWebAgentStop` reaches it to call `Stop`, and the partial is re-rendered the same way). The manager call itself is `mgr.Rename(name, newName)`.

- [ ] **Step 3: Register the routes**

Add alongside the existing agent routes (matching their mux pattern):

```go
	mux.HandleFunc("POST /api/agent/{name}/rename", s.handleAPIAgentRename)
	mux.HandleFunc("POST /web/agent/{name}/rename", s.handleWebAgentRename)
```

- [ ] **Step 4: Add the inline rename affordance to the template**

In `internal/web/templates/partials/agents.html`, beside the existing Stop button inside the `{{range .Agents}}` block, add an inline rename form (htmx posts and swaps the agents partial). Match the surrounding markup/classes:

```html
<form class="agent-rename" hx-post="/web/agent/{{.Name}}/rename"
      hx-target="#agents" hx-swap="outerHTML"
      onsubmit="return this.new_name.value.trim() !== '';">
  <input type="text" name="new_name" placeholder="new name"
         aria-label="Rename {{.Name}}" />
  <button type="submit">Rename</button>
</form>
```

Confirm the `hx-target` id (`#agents`) matches the actual wrapper element id the stop button targets; use that exact id.

- [ ] **Step 5: Build + run web tests**

Run: `go build ./... && go test ./internal/web/ -race`
Expected: build succeeds; existing web tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers_agents.go internal/web/templates/partials/agents.html
git commit -m "feat(web): inline agent rename in dashboard"
```

---

## Task 9: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Format**

Run: `gofmt -l internal/ && goimports -l internal/ 2>/dev/null`
Expected: no files listed. If any are, run `gofmt -w` / `goimports -w` on them and amend the relevant commit.

- [ ] **Step 2: Full test suite with race**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 3: Lint (vet + staticcheck + gosec parity with CI)**

Run: `make lint`
Expected: clean. Address any finding before proceeding (CI runs gosec; the new exec of `tmux rename-session` goes through the seam using `exec.Command` with non-tainted args — if gosec flags G204, the args are constant flags plus validated names, annotate consistently with the existing `//nolint`/`#nosec` style already used for the other tmux exec calls in `process.go`).

- [ ] **Step 4: Manual smoke (local daemon)**

If a local daemon + a spawnable template is available:

```bash
# spawn, rename, verify the new name + that the tmux session followed, attach
leo agent list
leo agent rename <some-agent> smoke-rename
leo agent list                         # shows leo-smoke-rename
tmux ls | grep leo-smoke-rename        # session renamed, same pane
leo agent attach smoke-rename          # resolves + attaches
```

Expected: the agent appears under `leo-smoke-rename`, its tmux session was renamed (not recreated — check the session's creation time is unchanged via `tmux ls -F '#{session_name} #{session_created}'` before/after), and attach works. If a daemon/template is not available, note that manual smoke was skipped.

- [ ] **Step 5: Open the PR**

```bash
git push -u origin feat/agent-rename
gh pr create --fill --base main
```

Use a PR body summarizing: zero-restart rename via the live identity handle, CLI + web surfaces, and the test coverage. Include a test plan referencing `make test` / `make lint` and the manual smoke steps.

---

## Self-Review Notes

- **Spec coverage:** procIdentity (§1) → Task 3; superviseProcess rewrite (§2) → Task 4; RenameAgent (§3) → Task 4; Manager.Rename (§4) → Task 5; agentstore.Rename (§5) → Task 1; NormalizeAgentName (§6) → Task 2; daemon endpoint (§7) → Task 6; CLI (§8) → Task 7; web (§9) → Task 8; tests (§Testing) → distributed per task + Task 9. All covered.
- **Risk concentration:** Task 4 is the only behavior-changing refactor of existing code; it is split into compile-green sub-steps (struct field → call sites → function rewrites → build → new method) so a failure is localized.
- **Interface fan-out:** adding `RenameAgent` to `agent.Supervisor` and `Rename` to `daemon.AgentManager` will break any other fakes implementing those interfaces — Tasks 5/6 call this out and the full-package test runs will surface it.
