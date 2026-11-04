package consult

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// taskCall is one UpsertAuto the observer made.
type taskCall struct {
	Agent string
	Task  observe.AutoTask
}

type taskSink struct {
	calls []taskCall
	err   error // returned for a running upsert
}

func (s *taskSink) UpsertAuto(agent string, t observe.AutoTask) (observe.AgentTask, error) {
	s.calls = append(s.calls, taskCall{agent, t})
	if s.err != nil && t.State == observe.TaskRunning {
		return observe.AgentTask{}, s.err
	}
	return observe.AgentTask{}, nil
}

func (s *taskSink) take() []taskCall { c := s.calls; s.calls = nil; return c }

func newTaskHarness(known []string, recs ...Record) (*observeHarness, *taskSink) {
	h := newObserveHarness(recs...)
	sink := &taskSink{}
	isAgent := func(name string) bool {
		for _, k := range known {
			if k == name {
				return true
			}
		}
		return false
	}
	h.obs = NewDispatchObserver(h.records.get, h.pub,
		WithDispatchClock(func() time.Time { return h.now }),
		WithDispatchOwner(ownKey),
		WithDispatchTasks(sink, isAgent))
	return h, sink
}

func (h *observeHarness) setStatus(id string, status Status) {
	for i := range h.records.recs {
		if h.records.recs[i].ID == id {
			h.records.recs[i].Status = status
		}
	}
}

func taskID(id string) string { return "auto.dispatch." + id }

func TestDispatchTasksStatusTable(t *testing.T) {
	cases := []struct {
		name   string
		status Status
		input  bool
		want   observe.AutoTask
	}{
		{"queued", StatusQueued, false, observe.AutoTask{State: observe.TaskRunning, Status: "queued"}},
		{"running", StatusRunning, false, observe.AutoTask{State: observe.TaskRunning, Status: "running"}},
		{"waiting on you", StatusRunning, true, observe.AutoTask{State: observe.TaskRunning, Status: "waiting on you"}},
		{"needs_input status", StatusNeedsInput, false, observe.AutoTask{State: observe.TaskRunning, Status: "waiting on you"}},
		{"waiting", StatusWaiting, false, observe.AutoTask{State: observe.TaskRunning, Status: "waiting"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := agentDispatch("d1", "alpha", tc.status)
			if tc.input {
				rec.NeedsInput = &NeedsInput{Kind: "permission", RequestID: "r1"}
			}
			h, sink := newTaskHarness(nil, rec)

			h.obs.Tick()

			want := tc.want
			want.ID, want.Title, want.StartedAt = taskID("d1"), "n-d1 · implement", rec.StartedAt
			if got := sink.take(); !reflect.DeepEqual(got, []taskCall{{"alpha", want}}) {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestDispatchTasksEndStates(t *testing.T) {
	cases := map[Status]observe.TaskState{
		StatusDone: observe.TaskSucceeded, StatusIdle: observe.TaskSucceeded,
		StatusFailed: observe.TaskFailed, StatusTimeout: observe.TaskFailed,
		StatusCanceled: observe.TaskCancelled, StatusClosed: observe.TaskCancelled, StatusReleased: observe.TaskCancelled,
	}
	for status, want := range cases {
		t.Run(string(status), func(t *testing.T) {
			h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
			h.obs.Tick()
			sink.take()

			h.setStatus("d1", status)
			h.obs.Tick()

			got := sink.take()
			if len(got) != 1 || got[0].Task.State != want || got[0].Task.ID != taskID("d1") || got[0].Agent != "alpha" {
				t.Fatalf("got %+v, want one %s end", got, want)
			}
		})
	}
}

func TestDispatchTasksReleasingAnIdleRunChangesNothing(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	h.obs.Tick()
	h.setStatus("d1", StatusIdle)
	h.obs.Tick()
	sink.take()

	h.setStatus("d1", StatusReleased)
	h.obs.Tick()

	if got := sink.take(); len(got) != 0 {
		t.Fatalf("released while idle must not touch the task: %+v", got)
	}
}

func TestDispatchTaskFollowUpAfterIdleIsAFreshRun(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	h.obs.Tick()
	h.setStatus("d1", StatusIdle)
	h.obs.Tick()
	sink.take()
	h.now = h.now.Add(5 * time.Minute)

	h.setStatus("d1", StatusRunning)
	h.obs.Tick()

	got := sink.take()
	if len(got) != 1 || got[0].Task.State != observe.TaskRunning || !got[0].Task.StartedAt.Equal(h.now) {
		t.Fatalf("want a running upsert starting now, got %+v", got)
	}
}

func TestDispatchTasksSendOnlyWhatChanged(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	h.obs.Tick()
	sink.take()

	h.obs.Tick()
	if got := sink.take(); len(got) != 0 {
		t.Fatalf("an unchanged run re-sent: %+v", got)
	}
	h.setStatus("d1", StatusWaiting)
	h.obs.Tick()
	if got := sink.take(); len(got) != 1 || got[0].Task.Status != "waiting" {
		t.Fatalf("got %+v", got)
	}
}

func TestDispatchTasksIgnoreARunAlreadyOverWhenFirstSeen(t *testing.T) {
	done := agentDispatch("d1", "alpha", StatusDone)
	done.EndedAt = stateNow.Add(-10 * time.Second)
	h, sink := newTaskHarness(nil, done)

	h.obs.Tick()

	if got := sink.take(); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDispatchTaskNestedRunsRollUpToTheRootCallersAgent(t *testing.T) {
	root := agentDispatch("root", "alpha", StatusRunning)
	child := callerRecord("child", "dispatch:root", StatusRunning)
	child.ParentDispatchID = "root"
	grandchild := callerRecord("grand", "dispatch:child", StatusRunning)
	grandchild.ParentDispatchID = "child"
	h, sink := newTaskHarness(nil, root, child, grandchild)

	h.obs.Tick()

	agents := map[string]string{}
	for _, c := range sink.take() {
		agents[c.Task.ID] = c.Agent
	}
	want := map[string]string{taskID("root"): "alpha", taskID("child"): "alpha", taskID("grand"): "alpha"}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("got %v, want %v", agents, want)
	}
}

func TestDispatchTaskKeylessCallerFallsBackToCallerOnlyForAKnownAgent(t *testing.T) {
	known := callerRecord("known", "", StatusRunning)
	known.Caller = "alpha"
	unknown := callerRecord("unknown", "", StatusRunning)
	unknown.Caller = "someone-from-the-cli"
	h, sink := newTaskHarness([]string{"alpha"}, known, unknown)

	h.obs.Tick()

	got := sink.take()
	if len(got) != 1 || got[0].Agent != "alpha" || got[0].Task.ID != taskID("known") {
		t.Fatalf("got %+v", got)
	}
}

func TestDispatchTaskTitleFallsBackToTemplateAndOmitsAnEmptyRole(t *testing.T) {
	a := agentDispatch("a", "alpha", StatusRunning)
	a.Name = ""
	b := agentDispatch("b", "alpha", StatusRunning)
	b.Role = ""
	h, sink := newTaskHarness(nil, a, b)

	h.obs.Tick()

	titles := map[string]string{}
	for _, c := range sink.take() {
		titles[c.Task.ID] = c.Task.Title
	}
	if titles[taskID("a")] != "codex · implement" || titles[taskID("b")] != "n-b" {
		t.Fatalf("got %v", titles)
	}
}

func TestDispatchTaskIDHashesAnIDOutsideTheTaskAlphabet(t *testing.T) {
	rec := agentDispatch("D_Weird ID", "alpha", StatusRunning)
	h, sink := newTaskHarness(nil, rec)

	h.obs.Tick()

	got := sink.take()
	if len(got) != 1 || len(got[0].Task.ID) > observe.MaxTaskIDLen || got[0].Task.ID == taskID("D_Weird ID") {
		t.Fatalf("got %+v", got)
	}
}

func TestDispatchTaskOverTheCapIsSkippedWithoutRetryAndItsEndIgnored(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	sink.err = observe.ErrTaskCap
	h.obs.Tick()
	sink.take()

	h.setStatus("d1", StatusWaiting)
	h.obs.Tick()
	h.setStatus("d1", StatusDone)
	h.obs.Tick()

	if got := sink.take(); len(got) != 0 {
		t.Fatalf("a skipped run must neither retry nor end: %+v", got)
	}
}

func TestDispatchTaskForARecordThatVanishesWhileRunningIsCancelled(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	h.obs.Tick()
	sink.take()

	h.records.recs = nil
	h.obs.Tick()

	got := sink.take()
	if len(got) != 1 || got[0].Task.State != observe.TaskCancelled {
		t.Fatalf("got %+v", got)
	}
}

func TestDispatchTaskSinkErrorsOtherThanCapDoNotBreakTheTick(t *testing.T) {
	h, sink := newTaskHarness(nil, agentDispatch("d1", "alpha", StatusRunning))
	sink.err = errors.New("boom")

	h.obs.Tick() // must not panic or stop publishing

	if len(h.pub.events) == 0 {
		t.Fatal("dispatch events must still publish")
	}
}

// A rename moves the task in the store (Move); the run's end must follow the
// agent's current name, not the one the task was published under.
func TestDispatchTaskEndFollowsARenameBetweenPublishAndEnd(t *testing.T) {
	store := observe.NewProgressStore(nil, nil)
	rec := agentDispatch("d1", "alpha", StatusRunning)
	h := newObserveHarness(rec)
	owner := "alpha"
	h.obs = NewDispatchObserver(h.records.get, h.pub,
		WithDispatchClock(func() time.Time { return h.now }),
		WithDispatchOwner(func(string, string) (string, bool) { return owner, true }),
		WithDispatchTasks(store, nil))
	h.obs.Tick()

	store.Move("alpha", "beta")
	owner = "beta"
	h.setStatus("d1", StatusDone)
	h.obs.Tick()

	tasks := store.Tasks("beta")
	if len(tasks) != 1 || tasks[0].State != observe.TaskSucceeded {
		t.Fatalf("beta tasks = %+v (alpha: %+v)", tasks, store.Tasks("alpha"))
	}
	if got := store.Tasks("alpha"); len(got) != 0 {
		t.Fatalf("a task resurfaced under the old name: %+v", got)
	}
}
