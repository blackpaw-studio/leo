package agent

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingObservePublisher captures every event published to it, so tests
// can assert on the sequence without a real event bus.
type recordingObservePublisher struct {
	events []observe.Event
}

func (r *recordingObservePublisher) Publish(ev observe.Event) { r.events = append(r.events, ev) }

// TestStopSuspendedAgentPublishesAgentStopped is the regression test for
// finding #2: Manager.Stop only calls sup.StopAgent (which publishes
// agent_stopped) when the agent is live. A suspended agent is stopped purely
// via the agentstore, so — before this fix — nothing announced the
// transition, leaving a consumer that had received agent_state_changed{
// suspended} believing the agent was still around forever.
func TestStopSuspendedAgentPublishesAgentStopped(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents — the agent is suspended
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-x",
		Workspace: "/w",
		Branch:    "feat/x",
		SessionID: "sid",
		Suspended: true,
	})

	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetPublisher(pub)

	if err := m.Stop("leo-x"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if len(pub.events) != 1 {
		t.Fatalf("expected exactly 1 published event, got %d: %+v", len(pub.events), pub.events)
	}
	stopped, ok := pub.events[0].Payload.(*observe.AgentStoppedPayload)
	if !ok || pub.events[0].Type != observe.EventAgentStopped {
		t.Fatalf("expected AgentStopped, got %s (%T)", pub.events[0].Type, pub.events[0].Payload)
	}
	if stopped.Agent != "leo-x" {
		t.Fatalf("expected agent %q, got %q", "leo-x", stopped.Agent)
	}
}

// TestStopSharedAgentPublishesAgentStoppedBeforeRemoval covers the
// shared-workspace variant of finding #2: agentstore.Remove makes the agent
// vanish from the snapshot entirely, so the event is the only signal a
// stream-only consumer ever gets that it's gone.
func TestStopSharedAgentPublishesAgentStoppedBeforeRemoval(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // not live
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-shared",
		Workspace: "/w",
		Suspended: true,
		// Branch empty => shared workspace => Stop removes the record.
	})

	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetPublisher(pub)

	if err := m.Stop("leo-shared"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if len(pub.events) != 1 {
		t.Fatalf("expected exactly 1 published event, got %d: %+v", len(pub.events), pub.events)
	}
	if pub.events[0].Type != observe.EventAgentStopped {
		t.Fatalf("expected AgentStopped, got %s", pub.events[0].Type)
	}
}

// TestStopLiveAgentDoesNotDoublePublish guards against a duplicate
// agent_stopped: the live path already gets its event from
// sup.StopAgent (service.Supervisor.StopAgent publishes it), so Manager
// itself must not publish a second one.
func TestStopLiveAgentDoesNotDoublePublish(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-live": {Name: "leo-live", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-live", Workspace: "/w"})

	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetPublisher(pub)

	if err := m.Stop("leo-live"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// capturingSupervisor.StopAgent does not itself publish (it's a plain
	// test fake) — this asserts Manager doesn't add a publish of its own on
	// the live path, which would double up in production where
	// service.Supervisor.StopAgent already publishes.
	if len(pub.events) != 0 {
		t.Fatalf("expected no Manager-level publish on the live path, got %+v", pub.events)
	}
}

// TestRenameStoppedAgentPublishesStoppedThenSpawned is the regression test
// for finding #3: renaming a stopped agent skips sup.RenameAgent entirely
// (the live path's only producer of agent_stopped/agent_spawned for a
// rename), so the transition was previously silent.
func TestRenameStoppedAgentPublishesStoppedThenSpawned(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-stopped",
		Branch:     "feature",
		Stopped:    true,
		ClaudeArgs: []string{"--name", "leo-stopped"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}} // not live
	m := newTestManager(t, home, sup)
	pub := &recordingObservePublisher{}
	m.SetPublisher(pub)

	if _, err := m.Rename("leo-stopped", "leo-revived"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if len(pub.events) != 2 {
		t.Fatalf("expected 2 published events (stopped, spawned), got %d: %+v", len(pub.events), pub.events)
	}
	stopped, ok := pub.events[0].Payload.(*observe.AgentStoppedPayload)
	if !ok || pub.events[0].Type != observe.EventAgentStopped {
		t.Fatalf("expected first event AgentStopped, got %s (%T)", pub.events[0].Type, pub.events[0].Payload)
	}
	if stopped.Agent != "leo-stopped" {
		t.Fatalf("expected stopped agent %q, got %q", "leo-stopped", stopped.Agent)
	}
	spawned, ok := pub.events[1].Payload.(*observe.AgentSpawnedPayload)
	if !ok || pub.events[1].Type != observe.EventAgentSpawned {
		t.Fatalf("expected second event AgentSpawned, got %s (%T)", pub.events[1].Type, pub.events[1].Payload)
	}
	if spawned.Agent.Name != "leo-revived" {
		t.Fatalf("expected spawned agent name %q, got %q", "leo-revived", spawned.Agent.Name)
	}
	if spawned.Agent.Branch != "feature" {
		t.Fatalf("expected spawned agent to carry Branch %q from the stored record, got %q", "feature", spawned.Agent.Branch)
	}
}

// TestRenameRunningAgentDoesNotDoublePublish guards the live-rename path:
// service.Supervisor.RenameAgent already publishes both events in
// production, so Manager must not add its own on top.
func TestRenameRunningAgentDoesNotDoublePublish(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-old",
		ClaudeArgs: []string{"--name", "leo-old", "--model", "opus"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	m := newTestManager(t, home, sup)
	pub := &recordingObservePublisher{}
	m.SetPublisher(pub)

	if _, err := m.Rename("leo-old", "renamed-agent"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if len(pub.events) != 0 {
		t.Fatalf("expected no Manager-level publish on the live rename path, got %+v", pub.events)
	}
}
