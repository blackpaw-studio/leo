package observe

import (
	"testing"
	"time"
)

func (h *progressHarness) task(agent, id string) (AgentTask, bool) {
	for _, t := range h.store.Tasks(agent) {
		if t.ID == id {
			return t, true
		}
	}
	return AgentTask{}, false
}

func (h *progressHarness) lastEvent(agent, id string) *AgentTaskChangedPayload {
	var last *AgentTaskChangedPayload
	for _, p := range h.taskEvents(agent) {
		if p.ID == id {
			last = p
		}
	}
	return last
}

func TestProgressLifecycleStaleAfterFifteenMinutes(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})

	h.clock.Advance(15*time.Minute - time.Second)
	if got, _ := h.task("a", "t"); got.Stale {
		t.Fatal("stale too early")
	}
	h.clock.Advance(time.Second)

	got, _ := h.task("a", "t")
	if !got.Stale {
		t.Fatal("task should be stale after 15 minutes without an update")
	}
	if ev := h.lastEvent("a", "t"); ev.Task == nil || !ev.Task.Stale || ev.Task.Revision != got.Revision {
		t.Fatalf("stale flip must publish the new revision: %+v", ev)
	}
	if got.Revision != 2 {
		t.Fatalf("stale flip must bump revision, got %d", got.Revision)
	}
	if !got.UpdatedAt.Equal(progressT0) {
		t.Fatal("stale flip must not move updated_at")
	}
}

func TestProgressLifecycleUpdateClearsStaleAndRearms(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	h.clock.Advance(16 * time.Minute)

	got := h.up("a", TaskUpdate{ID: "t", Percent: ptr(1.0)})
	if got.Stale {
		t.Fatal("any update clears stale")
	}
	h.clock.Advance(14 * time.Minute)
	if cur, _ := h.task("a", "t"); cur.Stale {
		t.Fatal("stale timer must restart from the update, not the creation")
	}
	h.clock.Advance(time.Minute)
	if cur, _ := h.task("a", "t"); !cur.Stale {
		t.Fatal("task should go stale again 15 minutes after the update")
	}
}

func TestProgressLifecycleEndedTaskNeverGoesStale(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), State: TaskFailed})
	h.clock.Advance(time.Hour)
	if got, _ := h.task("a", "t"); got.Stale {
		t.Fatal("failed task must not be stale")
	}
}

func TestProgressLifecycleSucceededAndCancelledExpireAfterTenMinutes(t *testing.T) {
	for _, state := range []TaskState{TaskSucceeded, TaskCancelled} {
		t.Run(string(state), func(t *testing.T) {
			h := newProgressHarness(t)
			h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
			h.clock.Advance(time.Minute)
			h.up("a", TaskUpdate{ID: "t", State: state})

			h.clock.Advance(10*time.Minute - time.Second)
			if _, ok := h.task("a", "t"); !ok {
				t.Fatal("removed too early")
			}
			h.clock.Advance(time.Second)

			if _, ok := h.task("a", "t"); ok {
				t.Fatal("task should be removed 10 minutes after ended_at")
			}
			if ev := h.lastEvent("a", "t"); ev.Task != nil {
				t.Fatalf("expiry must publish task:null, got %+v", ev.Task)
			}
		})
	}
}

func TestProgressLifecycleFailedPersists(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), State: TaskFailed})
	h.clock.Advance(24 * time.Hour)
	if _, ok := h.task("a", "t"); !ok {
		t.Fatal("failed task must persist until dismissed")
	}
}

func TestProgressLifecycleFreshRunCancelsExpiry(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), State: TaskSucceeded})
	h.clock.Advance(5 * time.Minute)
	h.up("a", TaskUpdate{ID: "t", Percent: ptr(1.0)})

	h.clock.Advance(6 * time.Minute)

	if got, ok := h.task("a", "t"); !ok || got.State != TaskRunning {
		t.Fatal("the old run's expiry must not remove the fresh run")
	}
}

func TestProgressLifecycleDismissCancelsTimers(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), State: TaskFailed})
	if err := h.store.Dismiss("a", "t"); err != nil {
		t.Fatal(err)
	}
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T2")})
	h.clock.Advance(15*time.Minute - time.Second)
	if got, _ := h.task("a", "t"); got.Stale {
		t.Fatal("a dismissed task's timers must not touch its successor")
	}
}

func TestProgressLifecycleRemoveAgentClearsAndPublishes(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "x", Title: ptr("T")})
	h.up("a", TaskUpdate{ID: "y", Title: ptr("T"), State: TaskFailed})
	h.up("b", TaskUpdate{ID: "x", Title: ptr("T")})

	h.store.RemoveAgent("a")

	if len(h.store.Tasks("a")) != 0 || len(h.store.Tasks("b")) != 1 {
		t.Fatal("RemoveAgent must clear only that agent")
	}
	for _, id := range []string{"x", "y"} {
		if ev := h.lastEvent("a", id); ev == nil || ev.Task != nil {
			t.Fatalf("task %s: want a null event, got %+v", id, ev)
		}
	}
	if h.clock.pendingTimers() != 1 { // only b/x's stale timer remains
		t.Fatalf("pending timers = %d", h.clock.pendingTimers())
	}
	// A recreated agent starts clean: revision restarts after removal.
	if got := h.up("a", TaskUpdate{ID: "x", Title: ptr("T")}); got.Revision != 1 {
		t.Fatalf("revision = %d", got.Revision)
	}
}

func TestProgressLifecycleMoveRekeys(t *testing.T) {
	h := newProgressHarness(t)
	h.up("old", TaskUpdate{ID: "t", Title: ptr("T")})

	h.store.Move("old", "new")

	if len(h.store.Tasks("old")) != 0 {
		t.Fatal("old name must be empty")
	}
	if got, ok := h.task("new", "t"); !ok || got.Title != "T" {
		t.Fatal("task must move to the new name")
	}
	if ev := h.lastEvent("old", "t"); ev.Task != nil {
		t.Fatal("old name must get task:null")
	}
	if ev := h.lastEvent("new", "t"); ev == nil || ev.Task == nil {
		t.Fatal("new name must get the task")
	}
	h.clock.Advance(15 * time.Minute)
	if got, _ := h.task("new", "t"); !got.Stale {
		t.Fatal("moved task keeps its stale timer")
	}
	if ev := h.lastEvent("new", "t"); !ev.Task.Stale {
		t.Fatal("stale event must go out under the new name")
	}
}
