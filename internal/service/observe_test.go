package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingPublisher captures every event published to it, for assertions.
// Guarded by a mutex: SpawnAgent's supervise goroutine may keep publishing
// (e.g. subsequent restart attempts) concurrently with a test reading back
// what it captured so far.
type recordingPublisher struct {
	mu     sync.Mutex
	events []observe.Event
}

func (r *recordingPublisher) Publish(ev observe.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// Events returns a snapshot copy safe to inspect without racing Publish.
func (r *recordingPublisher) Events() []observe.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]observe.Event, len(r.events))
	copy(out, r.events)
	return out
}

func TestSetStateWithNoPublisherDoesNotPanic(t *testing.T) {
	// Arrange
	sv := NewSupervisor(context.Background())
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "starting"}
	sv.mu.Unlock()

	// Act + Assert: no publisher configured must be a safe no-op.
	sv.setState("agent-a", "running")
}

func TestSetStatePublishesAgentStateChanged(t *testing.T) {
	// Arrange
	sv := NewSupervisor(context.Background())
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "starting", Restarts: 2}
	sv.mu.Unlock()

	// Act
	sv.setState("agent-a", "running")

	// Assert
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != observe.EventAgentStateChanged {
		t.Fatalf("expected EventAgentStateChanged, got %s", ev.Type)
	}
	payload, ok := ev.Payload.(*observe.AgentStateChangedPayload)
	if !ok {
		t.Fatalf("expected AgentStateChangedPayload, got %T", ev.Payload)
	}
	if payload.Agent != "agent-a" || payload.Status != observe.StatusRunning || payload.Restarts != 2 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestSetStateForUnknownAgentDoesNotPublish(t *testing.T) {
	// Arrange
	sv := NewSupervisor(context.Background())
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	// Act: setState on a name never registered in s.states is a no-op today —
	// it must stay that way for events too.
	sv.setState("ghost", "running")

	// Assert
	if len(pub.events) != 0 {
		t.Fatalf("expected no event for an unknown agent, got %d", len(pub.events))
	}
}

func TestIncrementRestartsPublishesAgentStateChanged(t *testing.T) {
	// Arrange
	sv := NewSupervisor(context.Background())
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "restarting", Restarts: 0}
	sv.mu.Unlock()

	// Act
	sv.incrementRestarts("agent-a")

	// Assert
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	payload, ok := pub.events[0].Payload.(*observe.AgentStateChangedPayload)
	if !ok {
		t.Fatalf("expected AgentStateChangedPayload, got %T", pub.events[0].Payload)
	}
	if payload.Restarts != 1 {
		t.Fatalf("expected restarts=1, got %d", payload.Restarts)
	}
}

func TestSpawnAgentPublishesAgentSpawned(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	sv.homePath = t.TempDir()
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	// Act
	err := sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:    "agent-a",
		WorkDir: "/tmp/agent-a",
		Harness: "claude",
	})
	if err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	// Assert: SpawnAgent publishes synchronously before the supervise
	// goroutine starts, so it is always the first event — even though that
	// goroutine may itself publish further state-change events shortly after
	// (e.g. once it reaches its first setState("running")).
	events := pub.Events()
	if len(events) == 0 {
		t.Fatalf("expected at least 1 event, got 0")
	}
	ev := events[0]
	if ev.Type != observe.EventAgentSpawned {
		t.Fatalf("expected EventAgentSpawned, got %s", ev.Type)
	}
	payload, ok := ev.Payload.(*observe.AgentSpawnedPayload)
	if !ok {
		t.Fatalf("expected AgentSpawnedPayload, got %T", ev.Payload)
	}
	if payload.Agent.Name != "agent-a" || payload.Agent.Workspace != "/tmp/agent-a" || payload.Agent.Harness != "claude" {
		t.Fatalf("unexpected agent payload: %+v", payload.Agent)
	}
	if payload.Agent.Status != observe.StatusStarting {
		t.Fatalf("expected starting status, got %s", payload.Agent.Status)
	}
}

// TestSpawnAgentPublishesAgentSpawnedWithTemplateFields verifies that
// agent_spawned is populated from the agentstore record (saved by
// agent.Manager before SpawnAgent is called) and the defaults->template
// model cascade, not left blank — see docs/specs/2026-07-31-observability-api.md's
// note on agent_spawned completeness.
func TestSpawnAgentPublishesAgentSpawnedWithTemplateFields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	home := t.TempDir()
	sv.homePath = home

	if err := agentstore.Save(home, agentstore.Record{
		Name:      "agent-a",
		Template:  "coding",
		Repo:      "org/repo",
		Branch:    "feature",
		Workspace: "/tmp/agent-a",
	}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}

	cfgPath := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("templates:\n  coding:\n    model: opus\n"), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	sv.configPath = cfgPath

	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "agent-a", WorkDir: "/tmp/agent-a", Harness: "claude"}); err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	events := pub.Events()
	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}
	payload, ok := events[0].Payload.(*observe.AgentSpawnedPayload)
	if !ok {
		t.Fatalf("expected AgentSpawnedPayload, got %T", events[0].Payload)
	}
	if payload.Agent.Template != "coding" || payload.Agent.Repo != "org/repo" || payload.Agent.Branch != "feature" {
		t.Fatalf("unexpected agent payload: %+v", payload.Agent)
	}
	if payload.Agent.Model != "opus" {
		t.Fatalf("expected Model %q resolved via config cascade, got %q", "opus", payload.Agent.Model)
	}
}

// TestSpawnAgentPublishesAgentSpawnedGracefullyWithoutRecordOrConfig ensures
// a missing agentstore record or config (e.g. the no-context/name-collision
// tests below) doesn't panic or error SpawnAgent — the extra lookups must
// degrade to zero-valued fields, not fail the spawn.
func TestSpawnAgentPublishesAgentSpawnedGracefullyWithoutRecordOrConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	sv.homePath = t.TempDir()
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "agent-a", WorkDir: "/tmp/agent-a", Harness: "claude"}); err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	events := pub.Events()
	payload, ok := events[0].Payload.(*observe.AgentSpawnedPayload)
	if !ok {
		t.Fatalf("expected AgentSpawnedPayload, got %T", events[0].Payload)
	}
	if payload.Agent.Template != "" || payload.Agent.Repo != "" || payload.Agent.Branch != "" || payload.Agent.Model != "" {
		t.Fatalf("expected zero-valued optional fields absent a record/config, got %+v", payload.Agent)
	}
}

func TestStopAgentPublishesAgentStopped(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "running", Ephemeral: true}
	cancelFn := func() {}
	sv.cancels["agent-a"] = cancelFn
	sv.mu.Unlock()

	// Act
	if err := sv.StopAgent("agent-a"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	// Assert
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != observe.EventAgentStopped {
		t.Fatalf("expected EventAgentStopped, got %s", ev.Type)
	}
	payload, ok := ev.Payload.(*observe.AgentStoppedPayload)
	if !ok {
		t.Fatalf("expected AgentStoppedPayload, got %T", ev.Payload)
	}
	if payload.Agent != "agent-a" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

// TestSuspendAgentPublishesAgentStateChangedSuspended guards finding #3: a
// stream-only consumer must be able to tell "suspended" (coming back) apart
// from "gone" (observe.EventAgentStopped). SuspendAgent must publish
// agent_state_changed{status:"suspended"}, not agent_stopped.
func TestSuspendAgentPublishesAgentStateChangedSuspended(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "running", Ephemeral: true}
	sv.cancels["agent-a"] = func() {}
	sv.mu.Unlock()

	// Act
	if err := sv.SuspendAgent("agent-a"); err != nil {
		t.Fatalf("SuspendAgent: %v", err)
	}

	// Assert
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != observe.EventAgentStateChanged {
		t.Fatalf("expected EventAgentStateChanged, got %s", ev.Type)
	}
	payload, ok := ev.Payload.(*observe.AgentStateChangedPayload)
	if !ok {
		t.Fatalf("expected AgentStateChangedPayload, got %T", ev.Payload)
	}
	if payload.Agent != "agent-a" || payload.Status != observe.StatusSuspended {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// The agent must actually be gone from live state, exactly like StopAgent.
	if _, ok := sv.EphemeralAgents()["agent-a"]; ok {
		t.Fatal("expected agent removed from live state after suspend")
	}
}

// TestSuspendAgentPreservesRestartCount is the regression test for nit #6:
// SuspendAgent used to hardcode Restarts: 0 in its published payload,
// clobbering the real count for an agent that crash-looped before being
// suspended. The value must be read from the live state before it's deleted.
func TestSuspendAgentPreservesRestartCount(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "running", Ephemeral: true, Restarts: 7}
	sv.cancels["agent-a"] = func() {}
	sv.mu.Unlock()

	// Act
	if err := sv.SuspendAgent("agent-a"); err != nil {
		t.Fatalf("SuspendAgent: %v", err)
	}

	// Assert
	payload, ok := pub.events[0].Payload.(*observe.AgentStateChangedPayload)
	if !ok {
		t.Fatalf("expected AgentStateChangedPayload, got %T", pub.events[0].Payload)
	}
	if payload.Restarts != 7 {
		t.Fatalf("expected Restarts 7 to survive suspend, got %d", payload.Restarts)
	}
}

// TestSpawnAgentResumedPublishesAgentStateChanged guards finding #3's other
// half: resuming a suspended agent must surface as a state transition
// (agent_state_changed), not as a brand-new agent appearing
// (agent_spawned) — a consumer that saw the suspend already knows about
// this agent.
func TestSpawnAgentResumedPublishesAgentStateChanged(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.tmuxPath = "false"
	sv.claudePath = "false"
	sv.homePath = t.TempDir()
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	// Act
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:    "agent-a",
		WorkDir: "/tmp/agent-a",
		Harness: "claude",
		Resumed: true,
	}); err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	// Assert
	events := pub.Events()
	if len(events) == 0 {
		t.Fatalf("expected at least 1 event, got 0")
	}
	ev := events[0]
	if ev.Type != observe.EventAgentStateChanged {
		t.Fatalf("expected EventAgentStateChanged for a resumed spawn, got %s", ev.Type)
	}
	payload, ok := ev.Payload.(*observe.AgentStateChangedPayload)
	if !ok {
		t.Fatalf("expected AgentStateChangedPayload, got %T", ev.Payload)
	}
	if payload.Agent != "agent-a" || payload.Status != observe.StatusStarting {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}
