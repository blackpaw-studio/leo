package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// A client following /api/v1 events sees "restoring" in the snapshot and then
// no event for an agent whose restore fails or whose record is dropped, since
// those paths publish nothing themselves. By the time the daemon is ready,
// every agent that was restoring must have had a non-restoring status event.
func TestRestoreOutcomesResolveEveryRestoringAgent(t *testing.T) {
	home := t.TempDir()
	save := func(rec agentstore.Record) {
		t.Helper()
		rec.SpawnedAt = time.Now()
		if err := agentstore.Save(home, rec); err != nil {
			t.Fatalf("seed %s: %v", rec.Name, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "nas-not-mounted")
	save(agentstore.Record{Name: "shared-missing", Workspace: missing})
	save(agentstore.Record{Name: "worktree-gone", Workspace: missing, Branch: "feat/x", CanonicalPath: missing})
	save(agentstore.Record{Name: "healthy", Workspace: t.TempDir()})
	save(agentstore.Record{Name: "dormant", Workspace: t.TempDir(), Stopped: true, WakeOnMessage: true})

	bus := observe.NewBus()
	events, unsub, _ := bus.Subscribe(16)
	defer unsub()

	pending := pendingRestores(home)
	RestoreAgents(home, "", "", &fakeAgentSpawner{}, leomcp.Server{})
	live := map[string]daemon.ProcessStateInfo{"healthy": {Name: "healthy", Status: "running"}}
	publishRestoreOutcomes(home, pending, live, bus)

	got := map[string]observe.Event{}
	for len(events) > 0 {
		ev := <-events
		switch p := ev.Payload.(type) {
		case *observe.AgentStateChangedPayload:
			if p.Status == observe.StatusRestoring {
				t.Errorf("%s reported restoring after restore", p.Agent)
			}
			got[p.Agent] = ev
		case *observe.AgentStoppedPayload:
			got[p.Agent] = ev
		}
	}

	if p, ok := got["shared-missing"].Payload.(*observe.AgentStateChangedPayload); !ok || p.Status != observe.StatusStopped || p.WakeOnMessage {
		t.Errorf("failed restore: got %+v, want agent_state_changed stopped without wake", got["shared-missing"])
	}
	if _, ok := got["worktree-gone"].Payload.(*observe.AgentStoppedPayload); !ok {
		t.Errorf("dropped record: got %+v, want agent_stopped", got["worktree-gone"])
	}
	if p, ok := got["healthy"].Payload.(*observe.AgentStateChangedPayload); !ok || p.Status != observe.StatusRunning {
		t.Errorf("restored agent: got %+v, want agent_state_changed running", got["healthy"])
	}
	if _, ok := got["dormant"]; ok {
		t.Error("dormant agent was never restoring, but got an event")
	}
}
