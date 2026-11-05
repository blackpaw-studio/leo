package observe

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func (h *progressHarness) auto(agent string, a AutoTask) AgentTask {
	h.t.Helper()
	task, err := h.store.UpsertAuto(agent, a)
	if err != nil {
		h.t.Fatalf("UpsertAuto(%s, %s): %v", agent, a.ID, err)
	}
	return task
}

func autoRunning(id string) AutoTask {
	return AutoTask{ID: id, Title: "Build " + id, State: TaskRunning}
}

func TestAutoTasksHaveTheirOwnRunningCap(t *testing.T) {
	h := newProgressHarness(t)
	for i := range MaxTasksPerClass {
		h.up("a", TaskUpdate{ID: fmt.Sprintf("agent-%d", i), Title: ptr("T")})
		h.auto("a", autoRunning(fmt.Sprintf("auto.cmd.%02d", i)))
	}

	if _, err := h.store.UpsertAuto("a", autoRunning("auto.cmd.99")); !errors.Is(err, ErrTaskCap) {
		t.Fatalf("9th auto task: got %v, want ErrTaskCap", err)
	}
	if _, err := h.store.Upsert("a", TaskUpdate{ID: "agent-9", Title: ptr("T")}); !errors.Is(err, ErrTaskCap) {
		t.Fatalf("9th agent task: got %v, want ErrTaskCap", err)
	}
	if got := len(h.store.Tasks("a")); got != 2*MaxTasksPerClass {
		t.Fatalf("stored %d tasks, want %d", got, 2*MaxTasksPerClass)
	}
}

func TestAutoTaskCarriesSourceAndRealStart(t *testing.T) {
	h := newProgressHarness(t)
	started := progressT0.Add(-20 * time.Second)

	got := h.auto("a", AutoTask{ID: "auto.cmd.1", Title: "Build the app", State: TaskRunning, StartedAt: started})

	if got.Source != SourceAuto || got.Mode != TaskIndeterminate || got.State != TaskRunning {
		t.Fatalf("auto task shape wrong: %+v", got)
	}
	if !got.StartedAt.Equal(started) || !got.UpdatedAt.Equal(progressT0) {
		t.Fatalf("started_at must be the real start, updated_at the publish: %+v", got)
	}
	if agent := h.up("a", TaskUpdate{ID: "x", Title: ptr("T")}); agent.Source != SourceAgent {
		t.Fatalf("agent task source = %q", agent.Source)
	}
}

func TestAutoTaskStatusUpdatesInPlace(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.other.d1"))

	got := h.auto("a", AutoTask{ID: "auto.other.d1", Status: "waiting on you", State: TaskRunning})

	if got.Status != "waiting on you" || got.Title != "Build auto.other.d1" || got.Revision != 2 {
		t.Fatalf("update must keep the title and bump revision: %+v", got)
	}
}

func TestAutoFailedTaskGoneAtSixtySeconds(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))
	h.auto("a", AutoTask{ID: "auto.cmd.1", State: TaskFailed})

	h.clock.Advance(AutoExpireAfter - time.Second)
	if got, ok := h.task("a", "auto.cmd.1"); !ok || got.State != TaskFailed {
		t.Fatalf("failed auto task should still show at 59s: %+v %v", got, ok)
	}
	h.clock.Advance(time.Second)

	if _, ok := h.task("a", "auto.cmd.1"); ok {
		t.Fatal("failed auto task should be gone at 60s")
	}
	if ev := h.lastEvent("a", "auto.cmd.1"); ev == nil || ev.Task != nil {
		t.Fatalf("removal must publish a null task: %+v", ev)
	}
}

func TestAutoTasksNeverGoStale(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))

	h.clock.Advance(2 * StaleAfter)

	if got, _ := h.task("a", "auto.cmd.1"); got.Stale || got.State != TaskRunning {
		t.Fatalf("auto task must not go stale: %+v", got)
	}
}

func TestAutoDismissIsRefused(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))
	h.auto("a", AutoTask{ID: "auto.cmd.1", State: TaskSucceeded})

	if err := h.store.Dismiss("a", "auto.cmd.1"); !errors.Is(err, ErrTaskNotDismissable) {
		t.Fatalf("got %v, want ErrTaskNotDismissable", err)
	}
	if _, ok := h.task("a", "auto.cmd.1"); !ok {
		t.Fatal("refused dismiss must keep the task")
	}
}

func TestAutoEndedTasksAreBoundedAtEight(t *testing.T) {
	h := newProgressHarness(t)
	for i := range MaxTasksPerClass + 2 {
		id := fmt.Sprintf("auto.cmd.%02d", i)
		h.auto("a", autoRunning(id))
		h.auto("a", AutoTask{ID: id, State: TaskFailed})
		h.clock.Advance(time.Second)
	}

	tasks := h.store.Tasks("a")
	if len(tasks) != MaxTasksPerClass {
		t.Fatalf("kept %d ended auto tasks, want %d", len(tasks), MaxTasksPerClass)
	}
	if _, ok := h.task("a", "auto.cmd.00"); ok {
		t.Fatal("the oldest ended auto task should have been dropped")
	}
}

func TestAutoTaskRestartsAfterEnding(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.other.d1"))
	h.auto("a", AutoTask{ID: "auto.other.d1", State: TaskSucceeded})
	h.clock.Advance(10 * time.Second)
	restart := progressT0.Add(10 * time.Second)

	got := h.auto("a", AutoTask{ID: "auto.other.d1", State: TaskRunning, StartedAt: restart})

	if got.State != TaskRunning || got.EndedAt != nil || !got.StartedAt.Equal(restart) || got.Title != "Build auto.other.d1" {
		t.Fatalf("fresh run expected: %+v", got)
	}
	h.clock.Advance(AutoExpireAfter)
	if _, ok := h.task("a", "auto.other.d1"); !ok {
		t.Fatal("a restarted task must not be removed by the old run's expiry")
	}
}

func TestAutoRepeatedEndIsANoOp(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))
	first := h.auto("a", AutoTask{ID: "auto.cmd.1", State: TaskFailed})
	events := len(h.taskEvents("a"))

	again := h.auto("a", AutoTask{ID: "auto.cmd.1", State: TaskFailed})

	if again.Revision != first.Revision || len(h.taskEvents("a")) != events {
		t.Fatalf("repeat end must change nothing: %+v, %d events", again, len(h.taskEvents("a"))-events)
	}
}

func TestAgentTaskRejectsReservedAutoPrefix(t *testing.T) {
	_, err := TaskUpdate{ID: "auto.cmd.x", Title: ptr("T")}.Normalize()
	if !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("got %v, want ErrTaskInvalid", err)
	}
}

func TestRemoveAutoMatchingDropsOnlyMatches(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))
	h.auto("a", autoRunning("auto.other.d1"))
	h.up("a", TaskUpdate{ID: "build", Title: ptr("B")})

	removed := h.store.RemoveAutoRunning("a", "auto.cmd.")

	if len(removed) != 1 || removed[0] != "auto.cmd.1" {
		t.Fatalf("removed %v", removed)
	}
	if len(h.store.Tasks("a")) != 2 {
		t.Fatalf("tasks left: %+v", h.store.Tasks("a"))
	}
}

func TestRemoveNonFailedAlsoDropsAutoTasks(t *testing.T) {
	h := newProgressHarness(t)
	h.auto("a", autoRunning("auto.cmd.1"))
	h.auto("a", autoRunning("auto.cmd.2"))
	h.auto("a", AutoTask{ID: "auto.cmd.2", State: TaskFailed})

	h.store.RemoveNonFailed("a")

	if got := h.store.Tasks("a"); len(got) != 0 {
		t.Fatalf("auto tasks outlived a stop: %+v", got)
	}
}
