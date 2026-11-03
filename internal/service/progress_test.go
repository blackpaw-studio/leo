package service

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestStopAgentClearsTasks(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "echo"
	pub := &progressRecorder{}
	store := observe.NewProgressStore(nil, pub)
	sv.SetProgress(store)
	title := "Build"
	for _, agent := range []string{"eph", "other"} {
		if _, err := store.Upsert(agent, observe.TaskUpdate{ID: "build", Title: &title}); err != nil {
			t.Fatal(err)
		}
	}
	sv.mu.Lock()
	sv.states["eph"] = &ProcessState{Name: "eph", Status: "running", Ephemeral: true}
	sv.cancels["eph"] = func() {}
	sv.mu.Unlock()

	if err := sv.StopAgent("eph", false); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	if got := store.Tasks("eph"); len(got) != 0 {
		t.Fatalf("tasks after stop = %+v", got)
	}
	if got := store.Tasks("other"); len(got) != 1 {
		t.Fatalf("another agent's tasks must survive, got %+v", got)
	}
	if !pub.sawNullFor("eph", "build") {
		t.Fatal("stop must publish task:null for each cleared task")
	}
}

// progressRecorder records agent_task_changed payloads.
type progressRecorder struct {
	payloads []*observe.AgentTaskChangedPayload
}

func (r *progressRecorder) Publish(ev observe.Event) {
	if p, ok := ev.Payload.(*observe.AgentTaskChangedPayload); ok {
		r.payloads = append(r.payloads, p)
	}
}

func (r *progressRecorder) sawNullFor(agent, id string) bool {
	for _, p := range r.payloads {
		if p.Agent == agent && p.ID == id && p.Task == nil {
			return true
		}
	}
	return false
}
