package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
)

func TestSpawnAgentNameCollision(t *testing.T) {
	sv := NewSupervisor()
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
	sv := NewSupervisor()
	// ctx is nil

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "test-agent"})
	if err == nil {
		t.Fatal("expected error when context is nil")
	}
}

func TestSpawnAgentSetsEphemeralState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sv := NewSupervisor()
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
	sv := NewSupervisor()
	err := sv.StopAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestStopAgentRejectsNonEphemeral(t *testing.T) {
	sv := NewSupervisor()
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
	sv := NewSupervisor()
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
	sv := NewSupervisor()
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
	sv := NewSupervisor()
	sv.mu.Lock()
	sv.states["agent"] = &ProcessState{Name: "agent", Status: "running", Ephemeral: true}
	sv.mu.Unlock()

	states := sv.States()
	if !states["agent"].Ephemeral {
		t.Error("States() should propagate Ephemeral flag")
	}
}
