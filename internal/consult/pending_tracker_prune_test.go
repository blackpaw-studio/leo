package consult

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A prune that leaves a waiting turn with nothing pending ends that turn as
// if its background work had finished: it keeps what its Stop said and the
// run goes idle.
func TestPruneWhileWaitingClosesTurnAndLeavesWaiting(t *testing.T) {
	cases := map[string]func(t *testing.T, d *Dispatcher, id string){
		"superseded stop omits it": func(t *testing.T, d *Dispatcher, id string) {
			if err := d.ReportSupersededStop(id, claudeStop(t, "e5", "", map[string]any{"background_tasks": []any{}, "session_crons": []any{}})); err != nil {
				t.Fatal(err)
			}
		},
		"failed stop says it is gone": func(t *testing.T, d *Dispatcher, id string) {
			reportAll(t, d, id, taskStopFailed(t, "e5", "TaskStop", "task_id", "b1", "No task found with ID: b1"))
		},
	}
	for name, prune := range cases {
		t.Run(name, func(t *testing.T) {
			d, rt, id := startBridgedIdle(t)
			sent, err := d.Send(context.Background(), id, "run the loop")
			if err != nil {
				t.Fatal(err)
			}
			reportAll(t, d, id,
				bridgeStart(t, "e1", "t2", "run the loop", rt.lastCommandID()),
				bashStarted(t, "e2", "p-2", "tu-1", "b1", "while true; do sleep 5; done"),
				bridgeStop(t, "e3", "t2", "the loop is running"),
			)
			if rec := idleRecord(t, d, id); rec.Status != StatusWaiting {
				t.Fatalf("setup: status=%s", rec.Status)
			}
			prune(t, d, id)
			rec := idleRecord(t, d, id)
			if got := turnByID(rec, sent.TurnID); rec.Status != StatusIdle || got.Outcome != TurnFinished || got.Text != "the loop is running" || got.Pending != nil {
				t.Fatalf("status=%s turn=%+v", rec.Status, got)
			}
			if rec.PendingWork != nil || len(rec.BackgroundTasks) != 0 {
				t.Fatalf("pending=%+v tracked=%v", rec.PendingWork, rec.BackgroundTasks)
			}
		})
	}
}

// A prune lowers only the shells: other pending work keeps the run waiting.
func TestPruneKeepsOtherPendingKinds(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id,
		bashStarted(t, "e1", "p-1", "tu-1", "b1", "go build ./..."),
		claudeStop(t, "e2", "building", shellAndMonitor),
	)
	reportAll(t, d, id, taskStopFailed(t, "e3", "TaskStop", "task_id", "b1", "b1 is not running (status: killed)"))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusWaiting || rec.PendingWork.Summary() != "1 monitor" || rec.Turns[0].Outcome != "" {
		t.Fatalf("status=%s pending=%+v turn=%+v", rec.Status, rec.PendingWork, rec.Turns[0])
	}
}

// The notification that ends a tracked task still carries its waiting turn
// on: the prune must not close the turn the wake continues.
func TestTaskNotificationPromptStillResumesTurn(t *testing.T) {
	stopB1 := map[string]any{"background_tasks": []any{map[string]any{"id": "b1", "type": "shell", "status": "running"}}, "session_crons": []any{}}
	t.Run("unkeyed", func(t *testing.T) {
		now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
		d, _, id := startClaudeInteractive(t, &now)
		reportAll(t, d, id,
			bashStarted(t, "e1", "", "tu-1", "b1", "go build ./..."),
			claudeStop(t, "e2", "building", stopB1),
			claudeHook(t, "e3", "UserPromptSubmit", taskNotification),
		)
		rec := idleRecord(t, d, id)
		if rec.Status != StatusRunning || len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" || len(rec.BackgroundTasks) != 0 {
			t.Fatalf("status=%s turns=%+v tracked=%v", rec.Status, rec.Turns, rec.BackgroundTasks)
		}
	})
	t.Run("keyed", func(t *testing.T) {
		d, _, id, _ := startArmedClaude(t)
		reportAll(t, d, id,
			idSubmit(t, "u1", "a", openingText),
			bashStarted(t, "e1", "a", "tu-1", "b1", "go build ./..."),
			idHook(t, "s1", "Stop", "a", map[string]any{"last_assistant_message": "building", "background_tasks": stopB1["background_tasks"], "session_crons": []any{}}),
			idSubmit(t, "u2", "w", taskNotification),
		)
		rec := idleRecord(t, d, id)
		if rec.Status != StatusRunning || len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" || len(rec.BackgroundTasks) != 0 {
			t.Fatalf("status=%s turns=%+v tracked=%v", rec.Status, rec.Turns, rec.BackgroundTasks)
		}
	})
}

// A send to a waiting run names the ways out instead of promising the turn
// will carry on: the background work may never wake it.
func TestSendToWaitingRunNamesReleaseAndCancel(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, claudeStop(t, "e1", "building", shellAndMonitor))
	_, err := d.Send(context.Background(), id, "anything new?")
	if err == nil {
		t.Fatal("send to a waiting run was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "leo_release") || !strings.Contains(msg, "leo_cancel") || !strings.Contains(msg, "1 shell · 1 monitor") || strings.Contains(msg, "will continue") {
		t.Fatalf("rejection=%q", msg)
	}
}

// A prune lowers only the waits that own the pruned task: a later turn that
// starts and stops its own shell leaves an earlier turn's wait on another
// shell standing.
func TestPruneLowersOnlyTheTurnThatOwnsTheTask(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "watch X")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id,
		bridgeStart(t, "e1", "t2", "watch X", rt.lastCommandID()),
		bashStarted(t, "e2", "p-2", "tu-1", "bX", "watch x"),
		bridgeStop(t, "e3", "t2", "X is running"),
		bridgeStart(t, "e4", "t3", "[leo] dispatch d-child done", "notify-d-child-t3"),
		bashStarted(t, "e5", "p-3", "tu-2", "bY", "watch y"),
		taskStopped(t, "e6", "p-3", "tu-3", "bY"),
	)
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != "" || got.Pending.Summary() != "1 shell" || rec.Status != StatusRunning {
		t.Fatalf("stopping Y ended the wait on X: status=%s turn=%+v", rec.Status, got)
	}
	if ids := trackedIDs(rec); len(ids) != 1 || ids[0] != "bX" {
		t.Fatalf("tracked=%v", ids)
	}
}

// A waiting turn saved by an older leo carries no task ids: it loads as
// before and no prune lowers it, as no prune did then.
func TestPruneLeavesALegacyWaitAlone(t *testing.T) {
	var legacy Turn
	if err := json.Unmarshal([]byte(`{"turn_id":"d-1#1","source":"orchestrator","pending_work":{"tasks":{"shell":1}}}`), &legacy); err != nil || legacy.PendingTaskIDs != nil || legacy.Pending.Summary() != "1 shell" {
		t.Fatalf("legacy turn=%+v err=%v", legacy, err)
	}
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id,
		bashStarted(t, "e1", "", "tu-1", "b1", "sleep 500"),
		claudeStop(t, "e2", "waiting", map[string]any{"background_tasks": []any{map[string]any{"id": "b1", "type": "shell", "status": "running"}}, "session_crons": []any{}}),
	)
	d.mu.Lock()
	d.runs[id].record.Turns[0].PendingTaskIDs = legacy.PendingTaskIDs
	d.mu.Unlock()
	reportAll(t, d, id, taskStopped(t, "e3", "", "tu-2", "b1"))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusWaiting || rec.Turns[0].Outcome != "" || rec.PendingWork.Summary() != "1 shell" || len(rec.BackgroundTasks) != 0 {
		t.Fatalf("status=%s turn=%+v tracked=%v", rec.Status, rec.Turns[0], rec.BackgroundTasks)
	}
}
