package consult

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// bashStarted is the PostToolUse report of a Bash call that started
// background work, as Claude posts it: the task id rides in the tool
// response, and the shell hook names the prompt, not the bridge turn.
func bashStarted(t *testing.T, eventID, promptID, toolUseID, taskID, command string) HookReport {
	t.Helper()
	return idHook(t, eventID, "PostToolUse", promptID, map[string]any{
		"tool_name":     "Bash",
		"tool_use_id":   toolUseID,
		"tool_input":    map[string]any{"command": command, "run_in_background": true},
		"tool_response": map[string]any{"backgroundTaskId": taskID, "stdout": ""},
	})
}

func taskStopped(t *testing.T, eventID, promptID, toolUseID, taskID string) HookReport {
	t.Helper()
	return idHook(t, eventID, "PostToolUse", promptID, map[string]any{
		"tool_name":     "TaskStop",
		"tool_use_id":   toolUseID,
		"tool_input":    map[string]any{"task_id": taskID},
		"tool_response": map[string]any{"message": "stopped"},
	})
}

func trackedIDs(rec Record) []string {
	ids := make([]string, 0, len(rec.BackgroundTasks))
	for _, task := range rec.BackgroundTasks {
		ids = append(ids, task.ID)
	}
	return ids
}

// The incident: under CLAUDE_CONFIG_DIR the mod never sees the Stop hook, so
// the bridge's turn.complete carries no pending work even though Claude's
// Stop listed a shell. The PostToolUse that started the shell is the only
// witness, and it must hold the run in waiting.
func TestBridgedStopWithNoPendingButTrackedShellWaits(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "run the check")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id,
		bridgeStart(t, "e1", "t2", "run the check", rt.lastCommandID()),
		bashStarted(t, "e2", "p-2", "tu-1", "b1", "pnpm check"),
		bridgeStop(t, "e3", "t2", "the check is running in the background"),
	)
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != "" || rec.Status != StatusWaiting {
		t.Fatalf("stop with a tracked shell finished the turn: status=%s turn=%+v", rec.Status, got)
	}
	if rec.PendingWork == nil || rec.PendingWork.Tasks["shell"] != 1 || rec.PendingWork.Summary() != "1 shell" {
		t.Fatalf("pending work=%+v", rec.PendingWork)
	}
	if got := rec.BackgroundTasks; len(got) != 1 || got[0].ID != "b1" || got[0].ToolUseID != "tu-1" || got[0].Command != "pnpm check" || got[0].StartedAt.IsZero() {
		t.Fatalf("tracked tasks=%+v", got)
	}

	reportAll(t, d, id, bridgeWake(t, "e4", "t3", taskNotification))
	rec = idleRecord(t, d, id)
	if rec.Status != StatusRunning || len(rec.BackgroundTasks) != 0 || rec.PendingWork != nil {
		t.Fatalf("the notification did not wake the run: status=%s tracked=%v pending=%v", rec.Status, rec.BackgroundTasks, rec.PendingWork)
	}
	reportAll(t, d, id, bridgeStop(t, "e5", "t3", "check passed"))
	rec = idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != TurnFinished || got.Text != "the check is running in the background\n\ncheck passed" || rec.Status != StatusIdle {
		t.Fatalf("the continuation did not finish the turn: status=%s turn=%+v", rec.Status, got)
	}
}

// A shell Claude's Stop already lists and the tracker already holds is one
// shell, not two.
func TestStopListingATrackedShellCountsItOnce(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "go build ./..."))
	reportAll(t, d, id, claudeStop(t, "e2", "building", map[string]any{"background_tasks": []any{
		map[string]any{"id": "b1", "type": "shell", "status": "running", "command": "go build ./..."},
		map[string]any{"id": "m1", "type": "monitor", "status": "running"},
	}}))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusWaiting || rec.PendingWork == nil || rec.PendingWork.Tasks["shell"] != 1 || rec.PendingWork.Tasks["monitor"] != 1 {
		t.Fatalf("status=%s pending=%+v", rec.Status, rec.PendingWork)
	}
}

// A Stop that lists background work and leaves a tracked id out says that
// task is over: the turn finishes instead of waiting on a ghost.
func TestStopOmittingATrackedTaskClearsIt(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 5"))
	reportAll(t, d, id, claudeStop(t, "e2", "done", map[string]any{"background_tasks": []any{}, "session_crons": []any{}}))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusIdle || len(rec.BackgroundTasks) != 0 || rec.Turns[0].Outcome != TurnFinished {
		t.Fatalf("status=%s tracked=%v turn=%+v", rec.Status, rec.BackgroundTasks, rec.Turns[0])
	}
}

func TestTaskStopClearsATrackedTask(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id,
		bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 500"),
		bashStarted(t, "e2", "p-1", "tu-2", "b2", "sleep 600"),
		taskStopped(t, "e3", "p-1", "tu-3", "b1"),
	)
	if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 || got[0] != "b2" {
		t.Fatalf("tracked after TaskStop=%v", got)
	}
}

// Only the notification that names a task clears it: another task's
// completion leaves the run waiting.
func TestNotificationClearsOnlyItsOwnTask(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	if _, err := d.Send(context.Background(), id, "run both"); err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id,
		bridgeStart(t, "e1", "t2", "run both", rt.lastCommandID()),
		bashStarted(t, "e2", "p-2", "tu-1", "b1", "one"),
		bashStarted(t, "e3", "p-2", "tu-2", "b2", "two"),
		bridgeStop(t, "e4", "t2", "both running"),
		bridgeWake(t, "e5", "t3", taskNotification), // names b1
		bridgeStop(t, "e6", "t3", "one finished"),
	)
	rec := idleRecord(t, d, id)
	if got := trackedIDs(rec); len(got) != 1 || got[0] != "b2" || rec.Status != StatusWaiting {
		t.Fatalf("status=%s tracked=%v", rec.Status, got)
	}
}

// A user quoting a notification tag is typing, not a task finishing.
func TestTypedPromptQuotingATaskNotificationClearsNothing(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 500"))
	reportAll(t, d, id, claudeHook(t, "e2", "UserPromptSubmit", "what does <task-notification><task-id>b1</task-id></task-notification> mean?"))
	if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 {
		t.Fatalf("a typed prompt cleared the tracker: %v", got)
	}
}

// Under a bridge the shell Stop hook is superseded, but its task list is the
// only authoritative one: it prunes tasks that already ended.
func TestSupersededStopPrunesTrackedTasksItOmits(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	if _, err := d.Send(context.Background(), id, "go"); err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id,
		bridgeStart(t, "e1", "t2", "go", rt.lastCommandID()),
		bashStarted(t, "e2", "p-2", "tu-1", "b1", "one"),
		bashStarted(t, "e3", "p-2", "tu-2", "b2", "two"),
	)
	listed := idHook(t, "e4", "Stop", "p-2", map[string]any{"background_tasks": []any{map[string]any{"id": "b2", "type": "shell", "status": "running"}}})
	if err := d.ReportSupersededStop(id, listed); err != nil {
		t.Fatal(err)
	}
	if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 || got[0] != "b2" {
		t.Fatalf("tracked after the shell Stop=%v", got)
	}
	// A Stop that carries no task list says nothing about the tracker.
	bare := idHook(t, "e5", "Stop", "p-2", map[string]any{"last_assistant_message": "x"})
	if err := d.ReportSupersededStop(id, bare); err != nil {
		t.Fatal(err)
	}
	if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 {
		t.Fatalf("a Stop without a task list pruned the tracker: %v", got)
	}
}

func TestTrackedTasksSurviveARecordRoundTrip(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "pnpm check"))
	raw, err := json.Marshal(idleRecord(t, d, id))
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := trackedIDs(back); len(got) != 1 || got[0] != "b1" || back.BackgroundTasks[0].Command != "pnpm check" {
		t.Fatalf("round trip lost the tracker: %+v", back.BackgroundTasks)
	}
}

// An interrupt ends the turn however much is tracked; the tasks stay
// tracked for the wake that still comes.
func TestInterruptDoesNotWaitOnTrackedTasks(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id,
		idSubmit(t, "e0", "p-1", openingText),
		bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 500"),
		idHook(t, "e2", "Interrupt", "p-1", nil),
	)
	rec := idleRecord(t, d, id)
	if rec.Status != StatusIdle || rec.Turns[0].Outcome != TurnInterrupted || len(rec.BackgroundTasks) != 1 {
		t.Fatalf("status=%s tracked=%v turn=%+v", rec.Status, rec.BackgroundTasks, rec.Turns[0])
	}
}

// A shell-hook Stop that names no turn (an older claude) and lists nothing
// still waits on what the tracker holds.
func TestUnkeyedStopWithTrackedShellWaits(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "pnpm check"))
	reportAll(t, d, id, claudeStop(t, "e2", "running", nil))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusWaiting || rec.PendingWork == nil || rec.PendingWork.Tasks["shell"] != 1 || rec.Turns[0].Outcome != "" {
		t.Fatalf("status=%s pending=%+v turn=%+v", rec.Status, rec.PendingWork, rec.Turns[0])
	}
}

// A run that ends takes its tracker with it.
func TestTrackerIsClearedWhenTheRunEnds(t *testing.T) {
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, bashStarted(t, "e1", "p-1", "tu-1", "b1", "sleep 500"))
	reportAll(t, d, id, claudeHook(t, "e2", "SessionEnd", ""))
	advanceClock(&now, time.Hour)
	d.Sweep(now)
	if rec := idleRecord(t, d, id); len(rec.BackgroundTasks) != 0 {
		t.Fatalf("an ended run kept its tracker: %+v", rec.BackgroundTasks)
	}
}
