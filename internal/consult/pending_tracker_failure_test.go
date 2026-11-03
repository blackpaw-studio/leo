package consult

import (
	"testing"
	"time"
)

// taskStopFailed is the PostToolUseFailure report of a task-stopping call
// Claude refused, as Claude posts it: the reason rides in the error field.
func taskStopFailed(t *testing.T, eventID, tool, field, taskID, errText string) HookReport {
	t.Helper()
	return idHook(t, eventID, "PostToolUseFailure", "p-1", map[string]any{
		"tool_name":   tool,
		"tool_use_id": "tu-stop",
		"tool_input":  map[string]any{field: taskID},
		"error":       errText,
	})
}

// A stop Claude refuses because the task is already gone says the task is
// over just as a successful stop does.
func TestFailedTaskStopForGoneTaskPrunes(t *testing.T) {
	gone := []string{
		"No task found with ID: b1",
		"No task found with ID: b1. Did you mean: b2?",
		"b1 is not running (status: completed)",
		"Task not found",
	}
	stops := []struct{ tool, field string }{{"TaskStop", "task_id"}, {"KillShell", "shell_id"}}
	for _, stop := range stops {
		for _, errText := range gone {
			t.Run(stop.tool+"/"+errText, func(t *testing.T) {
				now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
				d, _, id := startClaudeInteractive(t, &now)
				reportAll(t, d, id,
					bashStarted(t, "e1", "p-1", "tu-1", "b1", "while true; do sleep 5; done"),
					taskStopFailed(t, "e2", stop.tool, stop.field, "b1", errText),
				)
				if got := trackedIDs(idleRecord(t, d, id)); len(got) != 0 {
					t.Fatalf("a stop refused as gone left the task tracked: %v", got)
				}
			})
		}
	}
}

// Any other refusal leaves the task running, so it stays tracked.
func TestFailedTaskStopOtherErrorKeepsTask(t *testing.T) {
	for _, errText := range []string{
		"Task b1 is owned by another agent",
		"A task cannot stop itself",
		"Unsupported task type: monitor",
		"",
	} {
		t.Run(errText, func(t *testing.T) {
			now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
			d, _, id := startClaudeInteractive(t, &now)
			reportAll(t, d, id,
				bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 500"),
				taskStopFailed(t, "e2", "TaskStop", "task_id", "b1", errText),
			)
			if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 || got[0] != "b1" {
				t.Fatalf("tracked after refused stop=%v", got)
			}
		})
	}
}
