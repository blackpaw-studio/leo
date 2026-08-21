package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
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

func TestSpawnAgentSetsEphemeralState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sv := NewSupervisor(ctx)
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

func TestStopAgentNotFound(t *testing.T) {
	sv := NewSupervisor(context.Background())
	err := sv.StopAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestStopAgentRejectsNonEphemeral(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.mu.Lock()
	sv.states["static-proc"] = &ProcessState{Name: "static-proc", Status: "running", Ephemeral: false}
	sv.mu.Unlock()

	err := sv.StopAgent("static-proc")
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

	err := sv.StopAgent("eph-agent")
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", wantToken, spawner)
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
	if restored := RestoreAgents(home, "", "tok", spawner); restored != 1 {
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
	if restored := RestoreAgents(home, "", "tok", spawner); restored != 1 {
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
	restored := RestoreAgents(home, "", wantToken, spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
		restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	restored := RestoreAgents(home, "tmux", "", spawner)
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
	restored := RestoreAgents(home, "tmux", "", spawner)
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
	restored := RestoreAgents(home, "", "", spawner)
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
	RestoreAgents(home, "", "tok", spawner)

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
	restored := RestoreAgents(home, "", "", spawner)
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
	if restored := RestoreAgents(home, "", "", spawner); restored != 1 {
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
