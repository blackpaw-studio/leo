package agent

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestStopDormantAgentPublishesAgentStopped is the regression test for
// finding #2: Manager.Stop only calls sup.StopAgent (which publishes
// agent_stopped) when the agent is live. An already-dormant agent is stopped
// purely via the agentstore, so — before this fix — nothing announced the
// transition.
func TestStopDormantAgentPublishesAgentStopped(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents — the agent is already dormant
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-x",
		Workspace:     "/w",
		Branch:        "feat/x",
		SessionID:     "sid",
		Stopped:       true,
		WakeOnMessage: true,
	})

	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetPublisher(pub)

	if err := m.Stop("leo-x", StopOptions{}); err != nil {
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

// TestStopSharedAgentPublishesAgentStoppedAndKeepsRecord covers the
// shared-workspace variant: Stop no longer deletes the record (the central
// inversion of this change), but it must still publish exactly once so a
// stream-only consumer sees the transition.
func TestStopSharedAgentPublishesAgentStoppedAndKeepsRecord(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // not live
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-shared",
		Workspace:     "/w",
		Stopped:       true,
		WakeOnMessage: true,
		// Branch empty => shared workspace.
	})

	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetPublisher(pub)

	if err := m.Stop("leo-shared", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if len(pub.events) != 1 {
		t.Fatalf("expected exactly 1 published event, got %d: %+v", len(pub.events), pub.events)
	}
	if pub.events[0].Type != observe.EventAgentStopped {
		t.Fatalf("expected AgentStopped, got %s", pub.events[0].Type)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["leo-shared"]; !ok {
		t.Fatal("shared-workspace record must survive Stop")
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

	if err := m.Stop("leo-live", StopOptions{}); err != nil {
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

// TestRenameStoppedAgentDoesNotAnnounceWhenPersistFails is the regression
// test for finding #5: the stopped-agent rename path used to announce
// agent_stopped/agent_spawned BEFORE agentstore.Rename ran, so a persist
// failure left every stream consumer believing the rename happened while the
// agent, in truth, still exists under its old name with no compensating
// event. Reproduced with a read-only agents.json: the load inside
// agentstore.Rename still succeeds (read access is untouched), but the
// write that persists the re-key fails, so Rename must return an error and
// publish nothing at all.
func TestRenameStoppedAgentDoesNotAnnounceWhenPersistFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode-based read-only simulation is POSIX-specific")
	}
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-stopped",
		Branch:     "feature",
		Stopped:    true,
		ClaudeArgs: []string{"--name", "leo-stopped"},
	})
	storePath := agentstore.FilePath(home)
	if err := os.Chmod(storePath, 0o400); err != nil {
		t.Fatalf("chmod store read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storePath, 0o600) })

	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}} // not live
	m := newTestManager(t, home, sup)
	pub := &recordingObservePublisher{}
	m.SetPublisher(pub)

	if _, err := m.Rename("leo-stopped", "leo-revived"); err == nil {
		t.Fatal("expected Rename to fail against a read-only store")
	}

	if len(pub.events) != 0 {
		t.Fatalf("expected no events published when persisting the rename failed, got %+v", pub.events)
	}

	recs, err := agentstore.Load(filepath.Clean(storePath))
	if err != nil {
		t.Fatalf("loading store after failed rename: %v", err)
	}
	if _, ok := recs["leo-stopped"]; !ok {
		t.Fatal("expected the agent to still exist under its old name after a failed rename")
	}
	if _, ok := recs["leo-revived"]; ok {
		t.Fatal("store must not show the new name when the rename never persisted")
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
