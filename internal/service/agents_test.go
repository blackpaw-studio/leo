package service

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

func TestSpawnAgentNameCollision(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.ctx = context.Background()
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
	sv := NewSupervisor(nil)

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "test-agent"})
	if err == nil {
		t.Fatal("expected error when context is nil")
	}
}

func TestSpawnAgentSetsEphemeralState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sv := NewSupervisor(context.Background())
	sv.ctx = ctx
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

func TestRestoreAgentsRemovesFailedSharedRecord(t *testing.T) {
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
	if _, ok := stored[rec.Name]; ok {
		t.Fatalf("shared record whose respawn failed should be removed; got %+v", stored)
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
			got := argsWithResume(tc.args, tc.sid)
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
