package service

import (
	"context"
	"testing"
	"time"

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

func seedTask(t *testing.T, store *observe.ProgressStore, agent, id string) {
	t.Helper()
	title := "Build"
	if _, err := store.Upsert(agent, observe.TaskUpdate{ID: id, Title: &title}); err != nil {
		t.Fatal(err)
	}
}

// A crash-restart revokes the launch's token, so the tasks it reported must
// not survive into the replacement launch and eat its cap.
func TestUnexpectedExitClearsTheLaunchsTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	defer stopLoops(cancel, sv)
	sv.SetAttention(observe.NewAttentionStore(nil))
	store := observe.NewProgressStore(nil, nil)
	sv.SetProgress(store)
	seedTask(t, store, "crashy", "build")

	spawnFakehook(t, sv, "crashy", true) // the session dies on its own: a crash-restart
	defer func() { _ = sv.StopAgent("crashy", false) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(store.Tasks("crashy")) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("tasks survived the crash-restart: %+v", store.Tasks("crashy"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Only the live generation clears: a goroutine of a stopped launch must not
// wipe the tasks of the agent that replaced it.
func TestClearProgressOnlyForTheLiveGeneration(t *testing.T) {
	sv := NewSupervisor(context.Background())
	store := observe.NewProgressStore(nil, nil)
	sv.SetProgress(store)
	seedTask(t, store, "a", "build")
	stale := newProcIdentity("a", nil)
	live := addIdentity(sv, "a")

	sv.clearProgress("a", stale)
	if len(store.Tasks("a")) != 1 {
		t.Fatal("a stale generation cleared the live agent's tasks")
	}
	sv.clearProgress("a", live)
	if len(store.Tasks("a")) != 0 {
		t.Fatal("the live generation must clear its tasks")
	}
}

func seedFailed(t *testing.T, store *observe.ProgressStore, agent, id string) {
	t.Helper()
	title := "Build"
	if _, err := store.Upsert(agent, observe.TaskUpdate{ID: id, Title: &title, State: observe.TaskFailed}); err != nil {
		t.Fatal(err)
	}
}

// An idle-suspend is a stop: running work is dropped, but a failure stays
// visible (and dismissable) while the agent sleeps.
func TestStopAgentKeepsFailedTasks(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = "echo"
	store := observe.NewProgressStore(nil, nil)
	sv.SetProgress(store)
	seedTask(t, store, "eph", "run")
	seedFailed(t, store, "eph", "bad")
	sv.mu.Lock()
	sv.states["eph"] = &ProcessState{Name: "eph", Status: "running", Ephemeral: true}
	sv.cancels["eph"] = func() {}
	sv.mu.Unlock()

	if err := sv.StopAgent("eph", true); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	got := store.Tasks("eph")
	if len(got) != 1 || got[0].ID != "bad" {
		t.Fatalf("tasks after idle-suspend = %+v, want only the failed one", got)
	}
}

// Same for a crash-restart: the generation guard still applies.
func TestClearProgressKeepsFailedTasks(t *testing.T) {
	sv := NewSupervisor(context.Background())
	store := observe.NewProgressStore(nil, nil)
	sv.SetProgress(store)
	seedTask(t, store, "a", "run")
	seedFailed(t, store, "a", "bad")
	stale := newProcIdentity("a", nil)
	live := addIdentity(sv, "a")

	sv.clearProgress("a", stale)
	if len(store.Tasks("a")) != 2 {
		t.Fatal("a stale generation cleared tasks")
	}
	sv.clearProgress("a", live)
	got := store.Tasks("a")
	if len(got) != 1 || got[0].ID != "bad" {
		t.Fatalf("tasks after crash-restart = %+v, want only the failed one", got)
	}
}
