package run

import (
	"strconv"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// publisher announces task run events on the observability event bus.
// Optional: unset (nil, the default) makes publishEvent a safe no-op, so
// existing Run/Preview callers are unaffected.
var publisher observe.Publisher

// runNow is injectable so tests get deterministic run IDs and durations.
var runNow = time.Now

// SetPublisher wires an observe.Publisher into the task runner.
func SetPublisher(p observe.Publisher) { publisher = p }

// publishEvent is a nil-safe no-op when no publisher has been configured.
func publishEvent(ev observe.Event) {
	if publisher == nil {
		return
	}
	publisher.Publish(ev)
}

// newRunID builds a stable, deterministic-in-tests run ID: the task name plus
// its start timestamp, so no Date.now()-style nondeterminism leaks into the
// wire contract.
func newRunID(taskName string, startedAt time.Time) string {
	return taskName + "-" + strconv.FormatInt(startedAt.UnixNano(), 10)
}

// publishTaskRunStarted announces a task firing and returns the run's stable
// ID and start time so the caller can report the matching finish event.
func publishTaskRunStarted(taskName string) (id string, startedAt time.Time) {
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
			},
		},
	})
	return id, startedAt
}

// publishTaskRunFinished announces a task firing's outcome. reason is the
// history package's reason vocabulary (history.ReasonSuccess and friends);
// anything but success maps to observe.RunFailed, carrying reason verbatim in
// TaskRun.Error.
func publishTaskRunFinished(id, taskName string, startedAt time.Time, success bool, reason string) {
	endedAt := runNow()
	durationMS := endedAt.Sub(startedAt).Milliseconds()

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
			},
		},
	})
}
