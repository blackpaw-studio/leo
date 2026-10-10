package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/session"
)

func TestSpawnAgentNameCollision(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "echo" // won't actually run tmux properly, but won't crash
	sv.claudePath = "echo"
	sv.homePath = t.TempDir()

	// Pre-populate a state to simulate an existing process
	sv.mu.Lock()
	sv.states["existing"] = &ProcessState{Name: "existing", Status: "running"}
	sv.mu.Unlock()

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "existing"})
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if err.Error() != `process "existing" already exists` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSpawnAgentNoContext(t *testing.T) {
	// Construct with an explicitly-nil ctx to cover the defensive guard path.
	// The public NewSupervisor(ctx) API makes this hard to hit accidentally,
	// but we keep the internal check as belt-and-suspenders.
	var nilCtx context.Context
	sv := NewSupervisor(nilCtx)

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "test-agent"})
	if err == nil {
		t.Fatal("expected error when context is nil")
	}
}

// stopLoops cancels sv's context and waits out every supervise loop
// SpawnAgent started. Every test that spawns defers it, so no loop outlives
// its test: none writes into the test's removed temp dirs, or reads a
// package seam (initialBackoff, sessionPollInterval) a later test sets.
func stopLoops(cancel context.CancelFunc, sv *Supervisor) {
	cancel()
	sv.Wait()
}

// Wait returns only once the loop SpawnAgent started has returned: here the
// loop is blocked in a tmux new-session when its context ends, and records
// stopped only after that process is killed and reaped.
func TestWaitReturnsOnceSpawnedLoopsHaveReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	defer stopLoops(cancel, sv)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	sv.tmuxPath = filepath.Join(dir, "tmux")
	script := "#!/bin/sh\ncase \"$*\" in *new-session*) : > " + started + "; exec sleep 30;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	sv.claudePath = "false"
	sv.homePath = t.TempDir()
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "waited", WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new-session to block", func() bool { _, err := os.Stat(started); return err == nil })

	stopLoops(cancel, sv)

	sv.mu.RLock()
	status := sv.states["waited"].Status
	sv.mu.RUnlock()
	if status != "stopped" {
		t.Fatalf("status after Wait = %q, want stopped: Wait returned before the loop did", status)
	}
}

func TestSpawnAgentSetsEphemeralState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	sv := NewSupervisor(ctx)
	defer stopLoops(cancel, sv)
	sv.tmuxPath = "false" // will fail immediately, that's fine
	sv.claudePath = "false"
	sv.homePath = t.TempDir()

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:       "test-agent",
		ClaudeArgs: []string{"--model", "sonnet"},
		WorkDir:    t.TempDir(),
		WebPort:    "8370",
	})
	if err != nil {
		t.Fatalf("SpawnAgent() error: %v", err)
	}

	// Give goroutine a moment to start
	time.Sleep(50 * time.Millisecond)

	sv.mu.RLock()
	state, ok := sv.states["test-agent"]
	sv.mu.RUnlock()

	if !ok {
		t.Fatal("expected test-agent in states")
	}
	if !state.Ephemeral {
		t.Error("expected Ephemeral=true")
	}
}

// TestSpawnAgentRefusesUnsafeOpeningBrief confirms SpawnAgent refuses the
// launch outright (no state mutation, nothing scheduled) when
// OpeningBriefID resolves to a file that fails the Lstat safety check — here
// a symlink, which a shell $(cat ...) would otherwise happily follow. This is
// the "refuse to launch with a clear error" behavior the redesigned
// id+Lstat-verify mechanism requires, as opposed to the earlier design's
// silent drop-and-continue.
func TestSpawnAgentRefusesUnsafeOpeningBrief(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	sv.homePath = t.TempDir()

	id, err := claudeharness.GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	path, err := claudeharness.AgentBriefPathForID(sv.homePath, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("attacker-controlled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	err = sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:           "symlinked-agent",
		ClaudeArgs:     []string{"--model", "sonnet"},
		WorkDir:        t.TempDir(),
		OpeningBriefID: id,
	})
	if err == nil {
		t.Fatal("expected SpawnAgent to refuse a symlinked opening-brief file")
	}

	sv.mu.RLock()
	_, exists := sv.states["symlinked-agent"]
	sv.mu.RUnlock()
	if exists {
		t.Error("SpawnAgent must not register any state for a refused launch")
	}
}

// TestSpawnAgentRefusesMalformedOpeningBriefID mirrors the above for an
// OpeningBriefID that fails ID-shape validation before a path is even
// derived — e.g. a hand-edited agentstore record.
func TestSpawnAgentRefusesMalformedOpeningBriefID(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	sv.homePath = t.TempDir()

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:           "tainted-agent",
		ClaudeArgs:     []string{"--model", "sonnet"},
		WorkDir:        t.TempDir(),
		OpeningBriefID: "../../etc/passwd",
	})
	if err == nil {
		t.Fatal("expected SpawnAgent to refuse a malformed opening-brief id")
	}

	sv.mu.RLock()
	_, exists := sv.states["tainted-agent"]
	sv.mu.RUnlock()
	if exists {
		t.Error("SpawnAgent must not register any state for a refused launch")
	}
}

func TestStopAgentNotFound(t *testing.T) {
	sv := NewSupervisor(context.Background())
	err := sv.StopAgent("nonexistent", false)
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestStopAgentRejectsNonEphemeral(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.mu.Lock()
	sv.states["static-proc"] = &ProcessState{Name: "static-proc", Status: "running", Ephemeral: false}
	sv.mu.Unlock()

	err := sv.StopAgent("static-proc", false)
	if err == nil {
		t.Fatal("expected error for non-ephemeral process")
	}
	if err.Error() != `"static-proc" is not an ephemeral agent` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStopAgentRemovesState(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "echo" // won't find session, that's fine

	called := false
	cancelFn := func() { called = true }

	sv.mu.Lock()
	sv.states["eph-agent"] = &ProcessState{Name: "eph-agent", Status: "running", Ephemeral: true}
	sv.cancels["eph-agent"] = cancelFn
	sv.mu.Unlock()

	err := sv.StopAgent("eph-agent", false)
	if err != nil {
		t.Fatalf("StopAgent() error: %v", err)
	}

	if !called {
		t.Error("expected cancel function to be called")
	}

	sv.mu.RLock()
	_, inStates := sv.states["eph-agent"]
	_, inCancels := sv.cancels["eph-agent"]
	sv.mu.RUnlock()

	if inStates {
		t.Error("expected agent removed from states")
	}
	if inCancels {
		t.Error("expected agent removed from cancels")
	}
}

func TestEphemeralAgentsFiltersCorrectly(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.mu.Lock()
	sv.states["static"] = &ProcessState{Name: "static", Status: "running", Ephemeral: false}
	sv.states["eph-1"] = &ProcessState{Name: "eph-1", Status: "running", Ephemeral: true}
	sv.states["eph-2"] = &ProcessState{Name: "eph-2", Status: "stopped", Ephemeral: true}
	sv.mu.Unlock()

	agents := sv.EphemeralAgents()
	if len(agents) != 2 {
		t.Fatalf("EphemeralAgents() returned %d, want 2", len(agents))
	}
	if _, ok := agents["static"]; ok {
		t.Error("static process should not be in ephemeral agents")
	}
	if agents["eph-1"].Status != "running" {
		t.Errorf("eph-1 status = %q, want running", agents["eph-1"].Status)
	}
	if !agents["eph-2"].Ephemeral {
		t.Error("eph-2 should be marked ephemeral")
	}
}

func TestStatesIncludesEphemeralFlag(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.mu.Lock()
	sv.states["agent"] = &ProcessState{Name: "agent", Status: "running", Ephemeral: true}
	sv.mu.Unlock()

	states := sv.States()
	if !states["agent"].Ephemeral {
		t.Error("States() should propagate Ephemeral flag")
	}
}

func TestRestoreAgentsDropsWorktreeWithMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	// Seed a worktree record whose Workspace path does not exist on disk.
	rec := agentstore.Record{
		Name:          "leo-coding-owner-repo-feat-x",
		Template:      "coding",
		Repo:          "owner/repo",
		Workspace:     filepath.Join(t.TempDir(), "does-not-exist"),
		Branch:        "feat/x",
		CanonicalPath: filepath.Join(t.TempDir(), "canonical-missing"),
		ClaudeArgs:    []string{"--model", "sonnet"},
		WebPort:       "8370",
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed agentstore: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}

	stored, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("agentstore.Load: %v", err)
	}
	if _, ok := stored[rec.Name]; ok {
		t.Fatalf("expected record dropped, still present: %+v", stored)
	}
}

// mailDroppingSpawner is a spawner that also keeps agents' undelivered
// messages, as the supervisor does.
type mailDroppingSpawner struct {
	fakeAgentSpawner
	dropped []string
}

func (m *mailDroppingSpawner) DropAgentMail(name string) { m.dropped = append(m.dropped, name) }

// A worktree agent dropped at restore for its missing workspace is gone for
// good, so its undelivered messages go with it (and their senders are told).
func TestRestoreAgentsDroppingAWorktreeDropsItsMail(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:          "leo-coding-owner-repo-feat-y",
		Template:      "coding",
		Repo:          "owner/repo",
		Workspace:     filepath.Join(t.TempDir(), "does-not-exist"),
		Branch:        "feat/y",
		CanonicalPath: filepath.Join(t.TempDir(), "canonical-missing"),
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed agentstore: %v", err)
	}

	spawner := &mailDroppingSpawner{}
	RestoreAgents(home, "", "", spawner, leomcp.Server{})

	if !slices.Equal(spawner.dropped, []string{rec.Name}) {
		t.Fatalf("dropped mail of %v, want [%s]", spawner.dropped, rec.Name)
	}
}

// fakeAgentSpawner captures SpawnAgent calls so tests can assert what args
// RestoreAgents passed without spinning up the real supervisor (which would
// exec tmux).
type fakeAgentSpawner struct {
	calls   []daemon.AgentSpawnSpec
	nextErr error
}

func (f *fakeAgentSpawner) SpawnAgent(spec daemon.AgentSpawnSpec) error {
	f.calls = append(f.calls, spec)
	return f.nextErr
}

func TestRestoreAgentsSkipsStoppedWorktreeRecord(t *testing.T) {
	home := t.TempDir()
	wtDir := t.TempDir()
	// A worktree record the user explicitly stopped (Stopped=true). It must
	// survive restore — `leo agent prune` still needs it — but must NOT be
	// resurrected by SpawnAgent.
	rec := agentstore.Record{
		Name:          "leo-coding-owner-repo-feat-preserve",
		Template:      "coding",
		Repo:          "owner/repo",
		Workspace:     wtDir,
		Branch:        "feat/preserve",
		CanonicalPath: t.TempDir(),
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid-1"},
		SessionID:     "sid-1",
		WebPort:       "8370",
		SpawnedAt:     time.Now(),
		Stopped:       true,
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("expected 0 SpawnAgent calls for stopped record, got %d", len(spawner.calls))
	}

	stored, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := stored[rec.Name]; !ok {
		t.Fatalf("stopped worktree record should survive restore; got %+v", stored)
	}
}

func TestRestoreAgentsRespawnsSharedWithResume(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leo-coding-plain",
		Template:   "coding",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-42"},
		SessionID:  "sid-42",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const wantToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", wantToken, spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	got := spawner.calls[0].ClaudeArgs
	want := []string{"--model", "sonnet", "--resume", "sid-42"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
	if spawner.calls[0].WebToken != wantToken {
		t.Errorf("WebToken = %q, want %q", spawner.calls[0].WebToken, wantToken)
	}

	// Shared records that successfully respawn must remain in agents.json so
	// the next daemon restart can pick them up again.
	stored, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := stored[rec.Name]; !ok {
		t.Fatalf("shared record should survive successful respawn; got %+v", stored)
	}
}

// TestRestoreAgentsThreadsHarnessOntoSpawnSpec locks the harness-aware
// restore path: an empty Harness on the record (pre-migration) resolves to
// "claude" behavior (ResumeArgs rewrite applied), and the resolved harness
// name is threaded onto the SpawnAgent spec either way.
func TestRestoreAgentsThreadsHarnessOntoSpawnSpec(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leo-coding-plain",
		Template:   "coding",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-42"},
		SessionID:  "sid-42",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
		// Harness left empty: pre-migration record.
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "", "tok", spawner, leomcp.Server{}); restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	if got := spawner.calls[0].Harness; got != "" {
		t.Errorf("Harness = %q, want empty (record predates the field; caller treats empty as claude)", got)
	}
	// Empty-harness (claude) records must still get the ResumeArgs rewrite.
	want := []string{"--model", "sonnet", "--resume", "sid-42"}
	if !reflect.DeepEqual(spawner.calls[0].ClaudeArgs, want) {
		t.Errorf("ClaudeArgs = %v, want %v", spawner.calls[0].ClaudeArgs, want)
	}
}

// TestRestoreAgentsSkipsClaudeOnlyResumeLogicForNonClaude locks that a
// non-claude record's args and SessionID pass through RestoreAgents
// unchanged: no ResumeArgs rewrite, no claude jsonl LatestSession scan.
func TestRestoreAgentsSkipsClaudeOnlyResumeLogicForNonClaude(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leo-coding-codex",
		Template:   "coding",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"exec", "--json"},
		SessionID:  "thread-99",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
		Harness:    "codex",
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "", "tok", spawner, leomcp.Server{}); restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	got := spawner.calls[0]
	if got.Harness != "codex" {
		t.Errorf("Harness = %q, want codex", got.Harness)
	}
	if !reflect.DeepEqual(got.ClaudeArgs, rec.ClaudeArgs) {
		t.Errorf("ClaudeArgs = %v, want unchanged %v (no claude-only ResumeArgs rewrite)", got.ClaudeArgs, rec.ClaudeArgs)
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	if stored[rec.Name].SessionID != "thread-99" {
		t.Errorf("SessionID = %q, want unchanged thread-99 (no claude jsonl scan)", stored[rec.Name].SessionID)
	}
}

func TestRestoreAgentsLegacyRecordRespawnsWithoutResume(t *testing.T) {
	home := t.TempDir()
	// Pre-resume daemon versions never set SessionID. We still respawn so the
	// agent comes back; it just starts a fresh claude conversation.
	rec := agentstore.Record{
		Name:       "leo-coding-legacy",
		Template:   "coding",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet"},
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const wantToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", wantToken, spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	for _, a := range spawner.calls[0].ClaudeArgs {
		if a == "--resume" {
			t.Fatalf("legacy record should not produce --resume; got %v", spawner.calls[0].ClaudeArgs)
		}
	}
	if spawner.calls[0].WebToken != wantToken {
		t.Errorf("WebToken = %q, want %q", spawner.calls[0].WebToken, wantToken)
	}
}

// TestRestoreAgentsKeepsFailedSharedRecord locks the current behavior: a
// shared-workspace record whose respawn fails at boot must NOT be deleted (a
// transient failure would otherwise permanently destroy the agent's
// identity). It is kept with Stopped=true and a non-empty StoppedReason so
// it stays visible (Manager.List) and recoverable (`leo agent restart`).
func TestRestoreAgentsKeepsFailedSharedRecord(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leo-coding-doomed",
		Template:   "coding",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-x"},
		SessionID:  "sid-x",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{nextErr: fmt.Errorf("supervisor rejected spawn")}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}
	stored, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("shared record whose respawn failed should survive restore; got %+v", stored)
	}
	if !got.Stopped {
		t.Error("expected Stopped=true after a failed restore spawn")
	}
	if got.StoppedReason == "" {
		t.Error("expected a non-empty StoppedReason after a failed restore spawn")
	}
}

// TestRestoreAgentsKeepsSharedRecordWithMissingWorkspace locks the fix for a
// defect where a shared-workspace record's missing-workspace check was
// gated behind isWorktree, so a non-worktree record with a gone directory
// (e.g. an unmounted NAS at boot) went straight into a doomed tmux spawn
// instead of being caught here. It must now be kept with Stopped=true and a
// non-empty StoppedReason, and SpawnAgent must never be called for it.
func TestRestoreAgentsKeepsSharedRecordWithMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leo-coding-missing-ws",
		Template:   "coding",
		Workspace:  filepath.Join(t.TempDir(), "does-not-exist"),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-y"},
		SessionID:  "sid-y",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("expected 0 SpawnAgent calls for a missing-workspace shared record, got %d", len(spawner.calls))
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("shared record with missing workspace should survive restore; got %+v", stored)
	}
	if !got.Stopped {
		t.Error("expected Stopped=true for a missing-workspace shared record")
	}
	if got.StoppedReason == "" {
		t.Error("expected a non-empty StoppedReason for a missing-workspace shared record")
	}
}

// TestRestoreAgentsStoppedSurvivesMissingWorkspaceUnmodified covers both
// dormant flavors — a plain user stop and an idle-sweep stop (WakeOnMessage
// true or false, StoppedReason always empty) — of a non-worktree record with
// a missing workspace: the missing-workspace branch must not re-mark or
// mutate either, and neither is ever retried. This also locks the fix for a
// reviewer-caught defect: the missing-workspace branch used to run BEFORE the
// Stopped guard, so a dormant shared-workspace agent whose workspace was
// transiently missing at boot (e.g. a late NAS mount) got markFailedRestore'd,
// corrupting a healthy dormant record into a state that could turn into a
// silently lost agent.
func TestRestoreAgentsStoppedSurvivesMissingWorkspaceUnmodified(t *testing.T) {
	for _, wake := range []bool{false, true} {
		rec := agentstore.Record{
			Name:          "leo-stopped-missing-ws",
			Workspace:     filepath.Join(t.TempDir(), "does-not-exist"),
			SessionID:     "sid-stopped",
			Stopped:       true,
			WakeOnMessage: wake,
			SpawnedAt:     time.Now(),
		}
		home := t.TempDir()
		if err := agentstore.Save(home, rec); err != nil {
			t.Fatalf("seed: %v", err)
		}

		spawner := &fakeAgentSpawner{}
		restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
		if restored != 0 {
			t.Fatalf("wake=%v: expected 0 restored, got %d", wake, restored)
		}
		if len(spawner.calls) != 0 {
			t.Fatalf("wake=%v: expected 0 SpawnAgent calls for a dormant record, got %d", wake, len(spawner.calls))
		}

		stored, _ := agentstore.Load(agentstore.FilePath(home))
		got, ok := stored[rec.Name]
		if !ok {
			t.Fatalf("wake=%v: dormant record should survive restore; got %+v", wake, stored)
		}
		if got.StoppedReason != "" {
			t.Errorf("wake=%v: StoppedReason = %q, want unchanged empty", wake, got.StoppedReason)
		}
		if got.WakeOnMessage != wake {
			t.Errorf("wake=%v: WakeOnMessage = %v, want unchanged", wake, got.WakeOnMessage)
		}
	}
}

// TestRestoreAgentsRetriesFailedRestoreRecord locks the fix for a fleet-scale
// recovery gap: a record the system marked Stopped+StoppedReason after a
// prior failed boot-time restore (e.g. a NAS mount that was late once, but is
// mounted now) must be retried on the NEXT restore rather than permanently
// skipped — otherwise a transient outage requires an operator to run `leo
// agent restart` by hand for every affected agent, forever. A user-stopped
// record (StoppedReason empty) is the control: it must NOT be retried,
// exercised by TestRestoreAgentsStoppedSurvivesMissingWorkspaceUnmodified
// above.
func TestRestoreAgentsRetriesFailedRestoreRecord(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir() // present now — the transient condition cleared
	rec := agentstore.Record{
		Name:          "leo-coding-recovered",
		Template:      "coding",
		Workspace:     workspace,
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid-z"},
		SessionID:     "sid-z",
		WebPort:       "8370",
		Stopped:       true,
		StoppedReason: "workspace missing: " + workspace,
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored (retry succeeded), got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call (retry), got %d", len(spawner.calls))
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("recovered record should survive restore; got %+v", stored)
	}
	if got.Stopped {
		t.Error("Stopped should be cleared after a successful retry")
	}
	if got.StoppedReason != "" {
		t.Errorf("StoppedReason = %q, want empty after a successful retry", got.StoppedReason)
	}
}

// TestRestoreAgentsRetryReMarksOnRepeatFailure covers the "still broken"
// half of the retry contract: a failed-restore record retried into ANOTHER
// spawn failure must be re-marked Stopped+StoppedReason (not silently
// dropped, not left in some half-cleared state).
func TestRestoreAgentsRetryReMarksOnRepeatFailure(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	rec := agentstore.Record{
		Name:          "leo-coding-still-broken",
		Template:      "coding",
		Workspace:     workspace,
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid-w"},
		SessionID:     "sid-w",
		WebPort:       "8370",
		Stopped:       true,
		StoppedReason: "restore spawn failed: supervisor rejected spawn",
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{nextErr: fmt.Errorf("supervisor rejected spawn again")}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call (retry attempt), got %d", len(spawner.calls))
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("record should survive restore; got %+v", stored)
	}
	if !got.Stopped || got.StoppedReason == "" {
		t.Errorf("expected re-marked Stopped+StoppedReason after a repeat failure, got Stopped=%v StoppedReason=%q", got.Stopped, got.StoppedReason)
	}
}

// TestRestoreAgentsRepeatFailureSameReasonSkipsWrite covers a persistently
// broken agent (e.g. a dead NAS mount): every boot re-derives the identical
// "workspace missing: <path>" reason, and markFailedRestore must not perform
// a pointless agentstore.Save when the on-disk record already matches byte
// for byte. Verified via agents.json's mtime, since the record's content is
// identical either way — a content diff alone can't distinguish "skipped"
// from "wrote the same bytes".
func TestRestoreAgentsRepeatFailureSameReasonSkipsWrite(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "gone") // never created — always missing
	reason := "workspace missing: " + workspace
	rec := agentstore.Record{
		Name:          "leo-coding-perpetually-broken",
		Template:      "coding",
		Workspace:     workspace,
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid-p"},
		SessionID:     "sid-p",
		WebPort:       "8370",
		Stopped:       true,
		StoppedReason: reason,
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	path := agentstore.FilePath(home)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	// Give the filesystem clock room to distinguish "wrote again" from "left
	// alone" — a same-timestamp write would otherwise pass this test by
	// accident.
	time.Sleep(10 * time.Millisecond)

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 0 {
		t.Fatalf("expected 0 restored (still broken), got %d", restored)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("expected 0 SpawnAgent calls (workspace still missing), got %d", len(spawner.calls))
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("agents.json was rewritten for an unchanged failure reason: before=%v after=%v", before.ModTime(), after.ModTime())
	}

	stored, _ := agentstore.Load(path)
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("record should survive restore; got %+v", stored)
	}
	if !got.Stopped || got.StoppedReason != reason {
		t.Errorf("expected unchanged Stopped=true StoppedReason=%q, got Stopped=%v StoppedReason=%q", reason, got.Stopped, got.StoppedReason)
	}
}

// TestRestoreAgentsNonENOENTStatErrorDoesNotMarkRecord locks the fix for a
// reviewer-caught defect: any os.Stat error on rec.Workspace (permission
// denied, I/O error, a hung/timed-out mount) used to be treated identically
// to "does not exist", condemning a healthy-but-transiently-unreachable
// workspace to Stopped state. Only a confirmed fs.ErrNotExist may mark the
// record; any other stat error must fall through to a normal spawn attempt.
func TestRestoreAgentsNonENOENTStatErrorDoesNotMarkRecord(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permission bits; cannot force EACCES")
	}
	home := t.TempDir()
	parent := t.TempDir()
	workspace := filepath.Join(parent, "unreadable-child")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Deny traversal into parent so stat(workspace) fails with EACCES, not
	// ENOENT — the directory genuinely exists, it just can't be statted.
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) }) //nolint:errcheck

	rec := agentstore.Record{
		Name:       "leo-coding-eacces",
		Template:   "coding",
		Workspace:  workspace,
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-eacces"},
		SessionID:  "sid-eacces",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored (stat error must not block spawn), got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("record should survive restore; got %+v", stored)
	}
	if got.Stopped {
		t.Error("a non-ENOENT stat error must NOT mark the record Stopped")
	}
}

// When an agent's tmux session survived the daemon bounce (the common case for
// `leo update` / `leo service restart`, which SIGKILL the daemon but leave the
// independent tmux server running), RestoreAgents must re-adopt that live
// session rather than killing+respawning it — so a daemon restart no longer
// disrupts every running agent.
func TestRestoreAgentsAdoptsLiveSession(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leoterm",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet"},
		SessionID:  "sid-live",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	origHas := tmuxHasSession
	tmuxHasSession = func(_, _ string) bool { return true }
	defer func() { tmuxHasSession = origHas }()

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "tmux", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	if !spawner.calls[0].Adopt {
		t.Errorf("expected Adopt=true for a surviving live session, got false")
	}
}

// When no live session exists (a clean shutdown killed it), RestoreAgents must
// spawn fresh — Adopt=false — so the supervise loop creates a new session.
func TestRestoreAgentsFreshSpawnWhenSessionGone(t *testing.T) {
	home := t.TempDir()
	rec := agentstore.Record{
		Name:       "leoterm",
		Workspace:  t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet"},
		SessionID:  "sid-gone",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	origHas := tmuxHasSession
	tmuxHasSession = func(_, _ string) bool { return false }
	defer func() { tmuxHasSession = origHas }()

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "tmux", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("expected 1 SpawnAgent call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].Adopt {
		t.Errorf("expected Adopt=false when no live session exists, got true")
	}
}

func TestArgsWithResumeStripsExistingSessionFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		sid  string
		want []string
	}{
		{
			name: "strips --session-id and appends --resume",
			args: []string{"--model", "sonnet", "--session-id", "old"},
			sid:  "new",
			want: []string{"--model", "sonnet", "--resume", "new"},
		},
		{
			name: "strips existing --resume and appends fresh --resume",
			args: []string{"--model", "sonnet", "--resume", "old"},
			sid:  "new",
			want: []string{"--model", "sonnet", "--resume", "new"},
		},
		{
			name: "empty session ID strips flags without appending",
			args: []string{"--model", "sonnet", "--session-id", "old"},
			sid:  "",
			want: []string{"--model", "sonnet"},
		},
		{
			name: "no session flags, empty sid: args unchanged",
			args: []string{"--model", "sonnet"},
			sid:  "",
			want: []string{"--model", "sonnet"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.ResumeArgs(tc.args, tc.sid)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// If the user ran /clear inside an agent's claude session, a newer jsonl
// lives under ~/.claude/projects/<slug>/ than the SessionID agentstore knows
// about. RestoreAgents should resume the newest one and re-sync agentstore.
func TestRestoreAgentsPrefersLatestJSONLAfterClear(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })

	// Two jsonls: the one agentstore knows about (older) and a newer one
	// created by a post-/clear session that Leo never saw.
	oldJSONL := filepath.Join(projDir, "sid-old.jsonl")
	newJSONL := filepath.Join(projDir, "sid-new.jsonl")
	for _, p := range []string{oldJSONL, newJSONL} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldJSONL, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	rec := agentstore.Record{
		Name:       "leo-coding-post-clear",
		Template:   "coding",
		Workspace:  workspace,
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid-old"},
		SessionID:  "sid-old",
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	got := spawner.calls[0].ClaudeArgs
	want := []string{"--model", "sonnet", "--resume", "sid-new"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	if stored[rec.Name].SessionID != "sid-new" {
		t.Errorf("agentstore not re-synced: got %q, want sid-new", stored[rec.Name].SessionID)
	}
}

func TestRestoreAgentsSkipsStopped(t *testing.T) {
	home := t.TempDir()
	liveRec := agentstore.Record{
		Name:      "leo-live",
		Workspace: home,
		SessionID: "a",
		SpawnedAt: time.Now(),
	}
	stoppedRec := agentstore.Record{
		Name:          "leo-stopped",
		Workspace:     home,
		SessionID:     "b",
		Stopped:       true,
		WakeOnMessage: true,
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, liveRec); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := agentstore.Save(home, stoppedRec); err != nil {
		t.Fatalf("seed stopped: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	RestoreAgents(home, "", "tok", spawner, leomcp.Server{})

	spawned := map[string]bool{}
	for _, c := range spawner.calls {
		spawned[c.Name] = true
	}
	if spawned["leo-stopped"] {
		t.Fatal("stopped agent must not be respawned at boot, even with WakeOnMessage=true")
	}
	if !spawned["leo-live"] {
		t.Fatal("non-stopped agent should be restored")
	}
}

// TestRestoreAgentsHonorsNoResume covers the poison-recovery path: the prior
// supervisor run quick-exited while resuming, marked NoResume=true, and
// RestoreAgents must spawn fresh (no --resume) and clear the flag — even when
// a jsonl exists in the project directory that LatestSession would otherwise
// pick.
func TestRestoreAgentsHonorsNoResume(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	// Plant a jsonl that LatestSession would otherwise pick — proving
	// NoResume genuinely short-circuits the lookup.
	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })
	if err := os.WriteFile(filepath.Join(projDir, "sid-poison.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	rec := agentstore.Record{
		Name:       "leo-coding-poisoned",
		Template:   "coding",
		Workspace:  workspace,
		ClaudeArgs: []string{"--model", "sonnet", "--resume", "sid-poison"},
		SessionID:  "sid-poison",
		NoResume:   true,
		WebPort:    "8370",
		SpawnedAt:  time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	restored := RestoreAgents(home, "", "", spawner, leomcp.Server{})
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}

	got := spawner.calls[0].ClaudeArgs
	for _, a := range got {
		if a == "--resume" {
			t.Fatalf("NoResume agent should not get --resume; got %v", got)
		}
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	after := stored[rec.Name]
	if after.NoResume {
		t.Errorf("NoResume should be cleared after consumption")
	}
	if after.SessionID != "" {
		t.Errorf("SessionID should be cleared alongside NoResume; got %q", after.SessionID)
	}
}

// A template switch pins the record to the session it just restored for the
// arriving template. RestoreAgents must resume that id verbatim rather than the
// newest jsonl in the workspace — which, right after a switch between two
// claude templates, belongs to the template just left. The pin is one-shot, so
// it must also be cleared once consumed.
func TestRestoreAgentsHonorsSessionPinned(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })
	// The departing template's transcript: newest in the workspace, but written
	// BEFORE the switch, so the pin outranks it.
	otherTemplate := filepath.Join(projDir, "other-template.jsonl")
	if err := os.WriteFile(otherTemplate, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	beforeSwitch := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(otherTemplate, beforeSwitch, beforeSwitch); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	switchedAt := time.Now().Add(-time.Hour)

	rec := agentstore.Record{
		Name:            "leo-coding-switched",
		Template:        "review",
		Workspace:       workspace,
		ClaudeArgs:      []string{"--model", "opus"},
		SessionID:       "reviews-own-session",
		SessionPinnedAt: &switchedAt,
		WebPort:         "8370",
		SpawnedAt:       time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "", "", spawner, leomcp.Server{}); restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}

	got := spawner.calls[0].ClaudeArgs
	var resumed string
	for i, a := range got {
		if a == "--resume" && i+1 < len(got) {
			resumed = got[i+1]
		}
	}
	if resumed != "reviews-own-session" {
		t.Fatalf("resumed %q, want reviews-own-session (the pinned id, not the newest jsonl); args %v", resumed, got)
	}

	stored, _ := agentstore.Load(agentstore.FilePath(home))
	after := stored[rec.Name]
	if after.SessionPinnedAt != nil {
		t.Error("the switch pin should be cleared once consumed")
	}
	if after.SessionID != "reviews-own-session" {
		t.Errorf("SessionID = %q, want reviews-own-session", after.SessionID)
	}
}

// reservingSpawner is a fakeAgentSpawner that also takes RestoreAgents'
// bridge reservations, logging every call in order.
type reservingSpawner struct {
	log      []string
	failName string
}

func (r *reservingSpawner) SpawnAgent(spec daemon.AgentSpawnSpec) error {
	r.log = append(r.log, "spawn "+spec.Name)
	if spec.Name == r.failName {
		return errors.New("spawn failed")
	}
	return nil
}

func (r *reservingSpawner) ReserveAdoptions(names []string) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	r.log = append(r.log, "reserve "+strings.Join(sorted, ","))
}

func (r *reservingSpawner) ReleaseAdoption(name string) {
	r.log = append(r.log, "release "+name)
}

// The sessions about to be adopted reserve their bridge keys before any
// agent is spawned (a spawn may launch fresh and allocate a key at once),
// and one whose spawn fails gives its reservation back.
func TestRestoreAgentsReservesAdoptionsBeforeSpawning(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := agentstore.Save(home, agentstore.Record{Name: name, Workspace: t.TempDir(), SessionID: "sid-" + name}); err != nil {
			t.Fatal(err)
		}
	}
	origHas := tmuxHasSession
	tmuxHasSession = func(_, session string) bool { return session == "leo-a" || session == "leo-c" }
	defer func() { tmuxHasSession = origHas }()

	spawner := &reservingSpawner{failName: "c"}
	RestoreAgents(home, "tmux", "", spawner, leomcp.Server{})
	if len(spawner.log) == 0 || spawner.log[0] != "reserve a,c" {
		t.Fatalf("calls = %v, want the live sessions reserved first", spawner.log)
	}
	spawnC := slices.Index(spawner.log, "spawn c")
	if releaseC := slices.Index(spawner.log, "release c"); spawnC < 0 || releaseC < spawnC {
		t.Fatalf("calls = %v, want c's reservation released after its spawn failed", spawner.log)
	}
	if slices.Contains(spawner.log, "release a") {
		t.Fatalf("calls = %v: a spawned fine and keeps its reservation for its loop", spawner.log)
	}
}

// With nothing to restore the hub still learns that no adoption is coming.
func TestRestoreAgentsSettlesAdoptionWithNothingToRestore(t *testing.T) {
	spawner := &reservingSpawner{}
	RestoreAgents(t.TempDir(), "tmux", "", spawner, leomcp.Server{})
	if len(spawner.log) != 1 || spawner.log[0] != "reserve " {
		t.Fatalf("calls = %v, want one empty reservation", spawner.log)
	}
}

func seedEnvRecord(t *testing.T, home string, environments []string) {
	t.Helper()
	rec := agentstore.Record{
		Name: "leoterm", Workspace: t.TempDir(), Harness: "claude",
		ClaudeArgs: []string{"--model", "sonnet"}, SessionID: "sid", WebPort: "8370",
		Env: map[string]string{"CLAUDE_CONFIG_DIR": "/acct/b"}, Environments: environments, EnvLayered: true,
		SpawnedAt: time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatal(err)
	}
}

func withLiveTmuxSession(t *testing.T, live bool) {
	t.Helper()
	orig := tmuxHasSession
	tmuxHasSession = func(_, _ string) bool { return live }
	t.Cleanup(func() { tmuxHasSession = orig })
}

// A fresh boot-time launch must not replay an env snapshot whose named
// environment was deleted: the agent stays down, with the reason recorded.
func TestRestoreAgentsFailsClosedOnMissingEnvironmentForFreshSpawn(t *testing.T) {
	home := t.TempDir()
	seedEnvRecord(t, home, []string{"acct-b"})
	withLiveTmuxSession(t, false)
	cfg := &config.Config{HomePath: home}

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "tmux", "", spawner, leomcp.Server{}, WithRestoreConfig(cfg)); restored != 0 {
		t.Fatalf("restored = %d, want 0", restored)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawned despite a missing environment: %+v", spawner.calls)
	}
	got, _ := agentstore.Load(agentstore.FilePath(home))
	rec := got["leoterm"]
	if !rec.IsFailedRestore() || !strings.Contains(rec.StoppedReason, "acct-b") {
		t.Fatalf("record = stopped:%v reason:%q, want a failed-restore naming acct-b", rec.Stopped, rec.StoppedReason)
	}
}

// A surviving tmux session already runs with its env; adoption is unchanged.
func TestRestoreAgentsStillAdoptsWhenEnvironmentIsMissing(t *testing.T) {
	home := t.TempDir()
	seedEnvRecord(t, home, []string{"acct-b"})
	withLiveTmuxSession(t, true)

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "tmux", "", spawner, leomcp.Server{}, WithRestoreConfig(&config.Config{HomePath: home})); restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	if len(spawner.calls) != 1 || !spawner.calls[0].Adopt {
		t.Fatalf("calls = %+v, want one adoption", spawner.calls)
	}
}

func TestRestoreAgentsSpawnsFreshWhenEnvironmentsExist(t *testing.T) {
	home := t.TempDir()
	seedEnvRecord(t, home, []string{"acct-b"})
	withLiveTmuxSession(t, false)
	cfg := &config.Config{HomePath: home, Environments: map[string]map[string]string{"acct-b": {"CLAUDE_CONFIG_DIR": "/acct/b"}}}

	spawner := &fakeAgentSpawner{}
	if restored := RestoreAgents(home, "tmux", "", spawner, leomcp.Server{}, WithRestoreConfig(cfg)); restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	if got := spawner.calls[0].Env["CLAUDE_CONFIG_DIR"]; got != "/acct/b" {
		t.Fatalf("spawn env = %v", spawner.calls[0].Env)
	}
}
