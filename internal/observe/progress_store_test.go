package observe

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

var progressT0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type progressHarness struct {
	t     *testing.T
	clock *fakeClock
	pub   *recordingPublisher
	store *ProgressStore
}

func newProgressHarness(t *testing.T) *progressHarness {
	t.Helper()
	clock := newFakeClock(progressT0)
	pub := &recordingPublisher{}
	return &progressHarness{t: t, clock: clock, pub: pub, store: NewProgressStore(clock, pub)}
}

// up upserts and fails the test on error.
func (h *progressHarness) up(agent string, u TaskUpdate) AgentTask {
	h.t.Helper()
	task, err := h.store.Upsert(agent, u)
	if err != nil {
		h.t.Fatalf("Upsert(%s, %s): %v", agent, u.ID, err)
	}
	return task
}

// taskEvents returns the published agent_task_changed payloads for agent.
func (h *progressHarness) taskEvents(agent string) []*AgentTaskChangedPayload {
	var out []*AgentTaskChangedPayload
	for _, ev := range h.pub.events {
		if p, ok := ev.Payload.(*AgentTaskChangedPayload); ok && ev.Type == EventAgentTaskChanged && p.Agent == agent {
			out = append(out, p)
		}
	}
	return out
}

func TestProgressStoreCreateDefaults(t *testing.T) {
	h := newProgressHarness(t)

	got := h.up("alfred", TaskUpdate{ID: "build", Title: ptr("Xcode build")})

	if got.State != TaskRunning || got.Mode != TaskIndeterminate || got.Percent != nil || got.Stale {
		t.Fatalf("defaults wrong: %+v", got)
	}
	if !got.StartedAt.Equal(progressT0) || !got.UpdatedAt.Equal(progressT0) || got.EndedAt != nil {
		t.Fatalf("timestamps wrong: %+v", got)
	}
	if got.Revision != 1 {
		t.Fatalf("revision = %d", got.Revision)
	}
}

func TestProgressStoreCreateRequiresTitle(t *testing.T) {
	h := newProgressHarness(t)

	_, err := h.store.Upsert("alfred", TaskUpdate{ID: "build", Percent: ptr(5.0)})

	if !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("got %v", err)
	}
	if len(h.store.Tasks("alfred")) != 0 {
		t.Fatal("rejected create must not store a task")
	}
}

func TestProgressStoreInvalidUpdateIsRejected(t *testing.T) {
	h := newProgressHarness(t)
	_, err := h.store.Upsert("alfred", TaskUpdate{ID: "Bad ID", Title: ptr("x")})
	if !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestProgressStoreUpsertKeepsOmittedFields(t *testing.T) {
	h := newProgressHarness(t)
	h.up("alfred", TaskUpdate{ID: "build", Title: ptr("Xcode build"), Percent: ptr(10.0), Status: ptr("Compiling")})
	h.clock.Advance(time.Minute)

	got := h.up("alfred", TaskUpdate{ID: "build", Percent: ptr(40.0)})

	if got.Title != "Xcode build" || got.Status != "Compiling" || got.Mode != TaskDeterminate {
		t.Fatalf("omitted fields lost: %+v", got)
	}
	if *got.Percent != 40 {
		t.Fatalf("percent = %v", *got.Percent)
	}
	if !got.StartedAt.Equal(progressT0) || !got.UpdatedAt.Equal(progressT0.Add(time.Minute)) {
		t.Fatalf("timestamps wrong: %+v", got)
	}
}

func TestProgressStoreModeTransitions(t *testing.T) {
	h := newProgressHarness(t)
	got := h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), Percent: ptr(30.0)})
	if got.Mode != TaskDeterminate {
		t.Fatalf("percent must imply determinate: %+v", got)
	}

	got = h.up("a", TaskUpdate{ID: "t", Indeterminate: ptr(true)})
	if got.Mode != TaskIndeterminate || got.Percent != nil {
		t.Fatalf("indeterminate must clear percent: %+v", got)
	}

	got = h.up("a", TaskUpdate{ID: "t", Percent: ptr(55.0)})
	if got.Mode != TaskDeterminate || *got.Percent != 55 {
		t.Fatalf("percent must switch back to determinate: %+v", got)
	}

	got = h.up("a", TaskUpdate{ID: "t", Indeterminate: ptr(false)})
	if got.Mode != TaskDeterminate || *got.Percent != 55 {
		t.Fatalf("indeterminate=false keeps percent: %+v", got)
	}
}

func TestProgressStoreEmptyStatusClears(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), Status: ptr("busy")})
	got := h.up("a", TaskUpdate{ID: "t", Status: ptr("")})
	if got.Status != "" {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestProgressStoreETASecondsBecomesAbsolute(t *testing.T) {
	h := newProgressHarness(t)
	h.clock.Advance(30 * time.Second)

	got := h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), ETASeconds: ptr(90.0)})

	want := progressT0.Add(30*time.Second + 90*time.Second)
	if got.EtaAt == nil || !got.EtaAt.Equal(want) {
		t.Fatalf("eta_at = %v, want %v", got.EtaAt, want)
	}
}

func TestProgressStoreRevisionIncreasesOnEveryWrite(t *testing.T) {
	h := newProgressHarness(t)
	var last uint64
	for i := 0; i < 4; i++ {
		got := h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), Percent: ptr(float64(i))})
		if got.Revision <= last {
			t.Fatalf("revision %d after %d", got.Revision, last)
		}
		last = got.Revision
	}
}

func TestProgressStoreTerminalStateEndsTask(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), ETASeconds: ptr(60.0)})
	h.clock.Advance(time.Minute)

	got := h.up("a", TaskUpdate{ID: "t", State: TaskSucceeded})

	if got.State != TaskSucceeded || got.EndedAt == nil || !got.EndedAt.Equal(progressT0.Add(time.Minute)) {
		t.Fatalf("not ended: %+v", got)
	}
	if got.EtaAt != nil {
		t.Fatal("eta_at must clear once the task ends")
	}
}

func TestProgressStoreUpdateToEndedTaskStartsFreshRun(t *testing.T) {
	h := newProgressHarness(t)
	first := h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), Percent: ptr(80.0), Status: ptr("old")})
	h.clock.Advance(time.Minute)
	h.up("a", TaskUpdate{ID: "t", State: TaskFailed})
	h.clock.Advance(time.Minute)

	got := h.up("a", TaskUpdate{ID: "t", Percent: ptr(5.0)})

	if got.State != TaskRunning || got.EndedAt != nil {
		t.Fatalf("fresh run must be running: %+v", got)
	}
	if !got.StartedAt.Equal(progressT0.Add(2 * time.Minute)) {
		t.Fatalf("started_at = %v", got.StartedAt)
	}
	if got.Title != "T" || got.Status != "" || *got.Percent != 5 {
		t.Fatalf("fresh run keeps only the title: %+v", got)
	}
	if got.Revision <= first.Revision+1 {
		t.Fatalf("revision must keep increasing across runs: %d", got.Revision)
	}
}

func TestProgressStoreRepeatedTerminalStateIsNoOp(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	first := h.up("a", TaskUpdate{ID: "t", State: TaskSucceeded})
	events := len(h.pub.events)
	h.clock.Advance(time.Second)

	got := h.up("a", TaskUpdate{ID: "t", State: TaskSucceeded})

	if got.Revision != first.Revision || !got.EndedAt.Equal(*first.EndedAt) || len(h.pub.events) != events {
		t.Fatalf("repeat --done must not restart or republish: %+v", got)
	}
}

func TestProgressStoreRunningCap(t *testing.T) {
	h := newProgressHarness(t)
	for i := 0; i < MaxTasksPerClass; i++ {
		h.up("a", TaskUpdate{ID: fmt.Sprintf("t%d", i), Title: ptr("T")})
	}

	_, err := h.store.Upsert("a", TaskUpdate{ID: "t-extra", Title: ptr("T")})
	if !errors.Is(err, ErrTaskCap) {
		t.Fatalf("9th running: got %v", err)
	}
	// Updating an existing running task at the cap is fine.
	h.up("a", TaskUpdate{ID: "t0", Percent: ptr(1.0)})
	// Another agent has its own budget.
	h.up("b", TaskUpdate{ID: "t0", Title: ptr("T")})
	// Ending one frees a slot, and ended tasks do not count.
	h.up("a", TaskUpdate{ID: "t1", State: TaskSucceeded})
	h.up("a", TaskUpdate{ID: "t-extra", Title: ptr("T")})
	// A fresh run of an ended task counts again.
	if _, err := h.store.Upsert("a", TaskUpdate{ID: "t1", Percent: ptr(1.0)}); !errors.Is(err, ErrTaskCap) {
		t.Fatalf("restarting an ended task at the cap: got %v", err)
	}
}

func TestProgressStoreFailedTasksBounded(t *testing.T) {
	h := newProgressHarness(t)
	for i := 0; i <= MaxTasksPerClass; i++ {
		h.up("a", TaskUpdate{ID: fmt.Sprintf("f%d", i), Title: ptr("T"), State: TaskFailed})
		h.clock.Advance(time.Second)
	}

	tasks := h.store.Tasks("a")
	if len(tasks) != MaxTasksPerClass {
		t.Fatalf("kept %d failed tasks", len(tasks))
	}
	for _, task := range tasks {
		if task.ID == "f0" {
			t.Fatal("oldest failed task must be dropped")
		}
	}
	var dropped *AgentTaskChangedPayload
	for _, p := range h.taskEvents("a") {
		if p.ID == "f0" && p.Task == nil {
			dropped = p
		}
	}
	if dropped == nil {
		t.Fatal("dropping a task must publish task:null")
	}
}

func TestProgressStoreEndedTasksBounded(t *testing.T) {
	h := newProgressHarness(t)
	for i := 0; i <= MaxTasksPerClass; i++ {
		h.up("a", TaskUpdate{ID: fmt.Sprintf("s%d", i), Title: ptr("T"), State: TaskSucceeded})
		h.clock.Advance(time.Second)
	}
	if got := len(h.store.Tasks("a")); got != MaxTasksPerClass {
		t.Fatalf("kept %d ended tasks", got)
	}
}

func TestProgressStoreDismiss(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "run", Title: ptr("T")})
	h.up("a", TaskUpdate{ID: "bad", Title: ptr("T"), State: TaskFailed})
	h.up("a", TaskUpdate{ID: "ok", Title: ptr("T"), State: TaskSucceeded})
	h.up("a", TaskUpdate{ID: "off", Title: ptr("T"), State: TaskCancelled})

	if err := h.store.Dismiss("a", "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := h.store.Dismiss("zed", "bad"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("other agent: %v", err)
	}
	if err := h.store.Dismiss("a", "run"); !errors.Is(err, ErrTaskRunning) {
		t.Fatalf("running: %v", err)
	}
	for _, id := range []string{"ok", "off"} {
		if err := h.store.Dismiss("a", id); err != nil {
			t.Fatalf("dismiss %s: %v", id, err)
		}
		events := h.taskEvents("a")
		if last := events[len(events)-1]; last.ID != id || last.Task != nil {
			t.Fatalf("dismiss %s must publish task:null, got %+v", id, last)
		}
	}
	if err := h.store.Dismiss("a", "bad"); err != nil {
		t.Fatalf("failed: %v", err)
	}
	for _, task := range h.store.Tasks("a") {
		if task.ID == "bad" {
			t.Fatal("dismissed task still present")
		}
	}
	events := h.taskEvents("a")
	if last := events[len(events)-1]; last.ID != "bad" || last.Task != nil {
		t.Fatalf("dismiss must publish task:null, got %+v", last)
	}
}

func TestProgressStoreTasksAreSortedCopies(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "b", Title: ptr("B"), Percent: ptr(1.0)})
	h.clock.Advance(time.Second)
	h.up("a", TaskUpdate{ID: "a", Title: ptr("A")})

	tasks := h.store.Tasks("a")
	if tasks[0].ID != "b" || tasks[1].ID != "a" {
		t.Fatalf("want started_at order, got %s,%s", tasks[0].ID, tasks[1].ID)
	}
	*tasks[0].Percent = 99
	if p := h.store.Tasks("a")[0].Percent; *p != 1 {
		t.Fatal("Tasks must return copies")
	}
	if h.store.Tasks("nobody") != nil {
		t.Fatal("unknown agent has no tasks")
	}
}

func TestProgressStoreNilSafe(t *testing.T) {
	var s *ProgressStore
	if s.Tasks("a") != nil {
		t.Fatal("nil store has no tasks")
	}
	s.RemoveAgent("a")
	s.Move("a", "b")
	if _, err := s.Upsert("a", TaskUpdate{ID: "t", Title: ptr("T")}); err == nil {
		t.Fatal("nil store cannot accept updates")
	}
}

func TestProgressStoreUpsertGuardedRefusesWhenTheGuardFails(t *testing.T) {
	h := newProgressHarness(t)

	_, err := h.store.UpsertGuarded("a", TaskUpdate{ID: "t", Title: ptr("T")}, func() bool { return false })

	if !errors.Is(err, ErrTaskUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if len(h.store.Tasks("a")) != 0 || len(h.pub.events) != 0 {
		t.Fatal("a refused write must leave no trace")
	}
	if _, err := h.store.UpsertGuarded("a", TaskUpdate{ID: "t", Title: ptr("T")}, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
}
