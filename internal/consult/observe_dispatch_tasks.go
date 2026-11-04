package consult

import (
	"errors"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// DispatchTaskSink receives the auto tasks dispatch runs contribute to their
// caller's progress. *observe.ProgressStore implements it.
type DispatchTaskSink interface {
	UpsertAuto(agent string, t observe.AutoTask) (observe.AgentTask, error)
}

// WithDispatchTasks makes Tick publish every running leo_dispatch run as an
// auto task on its root caller's agent (nested runs roll up to the root).
// isAgent says whether a name is a supervised agent, for callers without a
// bridge launch (see WithDispatchOwner); nil allows none of them.
func WithDispatchTasks(sink DispatchTaskSink, isAgent func(name string) bool) DispatchObserverOption {
	return func(o *DispatchObserver) {
		o.taskSink, o.isAgent = sink, isAgent
		o.taskLast = map[string]sentTask{}
	}
}

// Task statuses a dispatch run shows while it runs.
const (
	taskStatusQueued    = "queued"
	taskStatusRunning   = "running"
	taskStatusWaiting   = "waiting"
	taskStatusNeedsYou  = "waiting on you"
	maxDispatchRootHops = 32
)

// dispatchPhase is where one run is, as a progress task sees it.
type dispatchPhase struct {
	running bool
	status  string            // running: the shown status
	end     observe.TaskState // not running: how the run ended
}

// sentTask is what the observer last sent for one dispatch run.
type sentTask struct {
	running bool
	status  string
	skipped bool // refused over the cap: neither retried nor ended
	agent   string
}

// phaseOf maps a dispatch's status (as shown, with child waits applied) to
// its phase; ok=false for a status that says nothing (unknown).
func phaseOf(shown Record) (dispatchPhase, bool) {
	if shown.Status == StatusNeedsInput || (shown.NeedsInput != nil && !shown.Status.Terminal()) {
		return dispatchPhase{running: true, status: taskStatusNeedsYou}, true
	}
	switch shown.Status {
	case StatusQueued:
		return dispatchPhase{running: true, status: taskStatusQueued}, true
	case StatusRunning, StatusSettling:
		return dispatchPhase{running: true, status: taskStatusRunning}, true
	case StatusWaiting:
		return dispatchPhase{running: true, status: taskStatusWaiting}, true
	case StatusDone, StatusIdle:
		return dispatchPhase{end: observe.TaskSucceeded}, true
	case StatusFailed, StatusTimeout:
		return dispatchPhase{end: observe.TaskFailed}, true
	case StatusCanceled, StatusClosed, StatusReleased:
		return dispatchPhase{end: observe.TaskCancelled}, true
	}
	return dispatchPhase{}, false
}

// dispatchTaskTitle is "<name or template> · <role>".
func dispatchTaskTitle(rec Record) string {
	name := rec.Name
	if name == "" {
		name = rec.Template
	}
	if name == "" {
		name = "dispatch"
	}
	if rec.Role == "" {
		return name
	}
	return name + " · " + rec.Role
}

// rootOf walks ParentDispatchID up to the run no other run started.
func rootOf(rec Record, byID map[string]Record) Record {
	for range maxDispatchRootHops {
		parent, ok := byID[parentDispatchID(rec)]
		if !ok {
			return rec
		}
		rec = parent
	}
	return rec
}

// taskAgent is the supervised agent a run's task belongs to: its root
// caller's live launch, else the root's Caller when that is a known agent.
func (o *DispatchObserver) taskAgent(rec Record, byID map[string]Record) (string, bool) {
	root := rootOf(rec, byID)
	if agent, ok := o.keyOwner(root); ok {
		return agent, true
	}
	if root.Caller != "" && o.isAgent != nil && o.isAgent(root.Caller) {
		return root.Caller, true
	}
	return "", false
}

// endAgent is the agent whose task a run's end applies to: the run's owner
// now (a rename since the task was published moved it), else the agent it was
// published under.
func (o *DispatchObserver) endAgent(rec Record, byID map[string]Record, published string) string {
	if agent, ok := o.taskAgent(rec, byID); ok {
		return agent
	}
	return published
}

// syncTasksLocked sends the auto-task changes the records imply since the
// last Tick: a run starting or changing phase upserts, a run ending ends its
// task. A run first seen already over, or ending without ever having run
// here, sends nothing.
func (o *DispatchObserver) syncTasksLocked(records []Record, now time.Time) {
	if o.taskSink == nil {
		return
	}
	shown := WithChildWait(records)
	byID := make(map[string]Record, len(records))
	for _, rec := range records {
		byID[rec.ID] = rec
	}
	seen := make(map[string]bool, len(records))
	for i, rec := range records {
		if rec.Kind != "dispatch" {
			continue
		}
		seen[rec.ID] = true
		phase, ok := phaseOf(shown[i])
		if !ok {
			continue
		}
		o.syncTaskLocked(rec, phase, byID, now)
	}
	for id, last := range o.taskLast {
		if seen[id] {
			continue
		}
		if last.running && !last.skipped {
			o.endTask(last.agent, id, observe.TaskCancelled)
		}
		delete(o.taskLast, id)
	}
}

func (o *DispatchObserver) syncTaskLocked(rec Record, phase dispatchPhase, byID map[string]Record, now time.Time) {
	last, had := o.taskLast[rec.ID]
	if !phase.running {
		if had && last.running && !last.skipped {
			o.endTask(o.endAgent(rec, byID, last.agent), rec.ID, phase.end)
		}
		if had && !last.running {
			return // already ended: an idle run released or closed changes nothing
		}
		o.taskLast[rec.ID] = sentTask{agent: last.agent}
		return
	}
	if had && last.running && (last.skipped || last.status == phase.status) {
		return
	}
	agent, ok := o.taskAgent(rec, byID)
	if !ok {
		return
	}
	started := rec.StartedAt
	if had && !last.running {
		started = now // a follow-up turn after idle: a fresh run
	}
	_, err := o.taskSink.UpsertAuto(agent, observe.AutoTask{
		ID: observe.AutoDispatchID(rec.ID), Title: dispatchTaskTitle(rec), Status: phase.status,
		State: observe.TaskRunning, StartedAt: started,
	})
	o.taskLast[rec.ID] = sentTask{running: true, status: phase.status, agent: agent, skipped: errors.Is(err, observe.ErrTaskCap)}
	if err != nil && !errors.Is(err, observe.ErrTaskCap) {
		delete(o.taskLast, rec.ID) // try again next Tick
	}
}

func (o *DispatchObserver) endTask(agent, dispatchID string, state observe.TaskState) {
	_, _ = o.taskSink.UpsertAuto(agent, observe.AutoTask{ID: observe.AutoDispatchID(dispatchID), State: state})
}
