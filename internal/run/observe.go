package run

import (
	"strconv"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// publisherMu guards publisher. SetPublisher is called once at daemon boot
// (and again by cron/task-runner goroutines that fire independently of that
// boot sequence), and publishEvent is read from those same goroutines — a
// plain package global read/written without synchronization is a data race
// by the Go memory model, safe only by accident of boot ordering.
var (
	publisherMu sync.RWMutex
	// publisher announces task run events on the observability event bus.
	// Optional: unset (nil, the default) makes publishEvent a safe no-op, so
	// existing Run/Preview callers are unaffected.
	publisher observe.Publisher
)

// runNow is injectable so tests get deterministic run IDs and durations.
var runNow = time.Now

// SetPublisher wires an observe.Publisher into the task runner.
func SetPublisher(p observe.Publisher) {
	publisherMu.Lock()
	defer publisherMu.Unlock()
	publisher = p
}

// CurrentPublisher returns the Publisher SetPublisher last installed, or nil
// if none has been set. It exists so a test can assert that a given seam
// (e.g. internal/cli/run.go's per-invocation SetPublisher call) actually ran,
// rather than only exercising the seam's target in isolation — see
// internal/cli/run_test.go for the regression this closes.
func CurrentPublisher() observe.Publisher {
	publisherMu.RLock()
	defer publisherMu.RUnlock()
	return publisher
}

// publishEvent is a nil-safe no-op when no publisher has been configured.
func publishEvent(ev observe.Event) {
	publisherMu.RLock()
	p := publisher
	publisherMu.RUnlock()
	if p == nil {
		return
	}
	p.Publish(ev)
}

// newRunID builds a stable, deterministic-in-tests run ID: the task name plus
// its start timestamp, so no Date.now()-style nondeterminism leaks into the
// wire contract.
func newRunID(taskName string, startedAt time.Time) string {
	return taskName + "-" + strconv.FormatInt(startedAt.UnixNano(), 10)
}

// runMeta carries the values actually resolved for one task firing —
// workspace, model, and harness — so every run event is self-describing.
// TaskRun deliberately denormalizes these rather than leaving them as a join
// through Snapshot.Tasks: see the doc comment on observe.TaskRun.
type runMeta struct {
	Workspace string
	Model     string
	Harness   string
}

// publishTaskRunStarted announces a task firing and returns the run's stable
// ID and start time so the caller can report the matching finish event.
func publishTaskRunStarted(taskName string, meta runMeta) (id string, startedAt time.Time) {
	startedAt = runNow()
	id = newRunID(taskName, startedAt)
	publishEvent(observe.Event{
		Type: observe.EventTaskRunStarted,
		Payload: &observe.TaskRunPayload{
			Run: observe.TaskRun{
				ID:        id,
				Task:      taskName,
				Status:    observe.RunRunning,
				StartedAt: startedAt,
				Workspace: meta.Workspace,
				Model:     meta.Model,
				Harness:   meta.Harness,
			},
		},
	})
	return id, startedAt
}

// publishTaskRunFinished announces a task firing's outcome and returns the
// end time and duration it computed, so the caller can record identical
// timing to history rather than taking a second, slightly different runNow()
// reading of its own. reason is the history package's reason vocabulary
// (history.ReasonSuccess and friends); anything but success maps to
// observe.RunFailed, carrying reason verbatim in TaskRun.Error. meta should
// be the same value passed to publishTaskRunStarted for this run, so the
// finish event carries identical Workspace/Model/Harness.
func publishTaskRunFinished(id, taskName string, startedAt time.Time, success bool, reason string, meta runMeta) (endedAt time.Time, durationMS int64) {
	endedAt = runNow()
	durationMS = endedAt.Sub(startedAt).Milliseconds()

	status := observe.RunSucceeded
	errText := ""
	eventType := observe.EventTaskRunSucceeded
	if !success {
		status = observe.RunFailed
		errText = reason
		eventType = observe.EventTaskRunFailed
	}

	publishEvent(observe.Event{
		Type: eventType,
		Payload: &observe.TaskRunPayload{
			Run: observe.TaskRun{
				ID:         id,
				Task:       taskName,
				Status:     status,
				StartedAt:  startedAt,
				EndedAt:    &endedAt,
				DurationMS: &durationMS,
				Error:      errText,
				Workspace:  meta.Workspace,
				Model:      meta.Model,
				Harness:    meta.Harness,
			},
		},
	})
	return endedAt, durationMS
}
