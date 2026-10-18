package consult

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// supersededStop is the shell Stop hook a bridge-owned run has dropped to
// ReportSupersededStop: it lists the task Claude still has running.
func supersededStop(t *testing.T, eventID string, tasks ...map[string]any) HookReport {
	t.Helper()
	listed := make([]any, 0, len(tasks))
	for _, task := range tasks {
		listed = append(listed, task)
	}
	b, err := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s-1", "background_tasks": listed, "session_crons": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

var runningShell = map[string]any{"id": "b1", "type": "shell", "status": "running", "command": "pnpm check"}

// startedBridgedTurn is a bridged run whose second turn has started and whose
// PostToolUse for a background Bash was lost: the tracker is empty.
func startedBridgedTurn(t *testing.T) (*Dispatcher, string, string) {
	t.Helper()
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "run the check")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "run the check", rt.lastCommandID()))
	return d, id, sent.TurnID
}

func assertWaitingOnShell(t *testing.T, d *Dispatcher, id, turnID string) {
	t.Helper()
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, turnID); got.Outcome != "" || rec.Status != StatusWaiting {
		t.Fatalf("not held waiting: status=%s turn=%+v", rec.Status, got)
	}
	if got := rec.BackgroundTasks; len(got) != 1 || got[0].ID != "b1" || got[0].Command != "pnpm check" {
		t.Fatalf("tracked tasks=%+v", got)
	}
	if rec.PendingWork == nil || rec.PendingWork.Tasks["shell"] != 1 {
		t.Fatalf("pending work=%+v", rec.PendingWork)
	}
}

// The superseded Stop overtakes the bridge's turn.complete: the task it lists
// is tracked before the turn closes, so the close waits.
func TestSupersededStopTracksAMissedTaskBeforeTurnComplete(t *testing.T) {
	d, id, turnID := startedBridgedTurn(t)
	if err := d.ReportSupersededStop(id, supersededStop(t, "s1", runningShell)); err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, bridgeStop(t, "e2", "t2", "the check is running"))
	assertWaitingOnShell(t, d, id, turnID)
}

// turn.complete closes the turn first: the late superseded Stop reopens it
// into waiting, so the run is not idle for the orchestrator to close.
func TestSupersededStopAfterTurnCompleteReturnsRunToWaiting(t *testing.T) {
	d, id, turnID := startedBridgedTurn(t)
	reportAll(t, d, id, bridgeStop(t, "e2", "t2", "the check is running"))
	if rec := idleRecord(t, d, id); rec.Status != StatusIdle {
		t.Fatalf("turn.complete with no pending work should close idle, status=%s", rec.Status)
	}
	if err := d.ReportSupersededStop(id, supersededStop(t, "s1", runningShell)); err != nil {
		t.Fatal(err)
	}
	assertWaitingOnShell(t, d, id, turnID)
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, turnID); len(got.WaitText) != 1 || got.WaitText[0] != "the check is running" {
		t.Fatalf("the closing words were not kept as wait text: %+v", got)
	}
	if closed, err := d.CloseFinished(rec.CallerSessionID); err != nil || len(closed) != 0 {
		t.Fatalf("a waiting run was closable: closed=%v err=%v", closed, err)
	}

	// The wake and its own Stop finish the turn with both texts.
	reportAll(t, d, id, bridgeWake(t, "e3", "t3", taskNotification), bridgeStop(t, "e4", "t3", "check passed"))
	rec = idleRecord(t, d, id)
	if got := turnByID(rec, turnID); got.Outcome != TurnFinished || got.Text != "the check is running\n\ncheck passed" || rec.Status != StatusIdle {
		t.Fatalf("status=%s turn=%+v", rec.Status, got)
	}
}

// The reopen is bounded: a Stop long after the turn closed does not drag a
// finished turn back.
func TestSupersededStopLongAfterTurnCompleteLeavesRunIdle(t *testing.T) {
	d, id, turnID := startedBridgedTurn(t)
	reportAll(t, d, id, bridgeStop(t, "e2", "t2", "done"))
	ended := turnByID(idleRecord(t, d, id), turnID).EndedAt
	now := ended.Add(lateStopReopenWindow + time.Second)
	d.now = func() time.Time { return now }
	if err := d.ReportSupersededStop(id, supersededStop(t, "s1", runningShell)); err != nil {
		t.Fatal(err)
	}
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, turnID); got.Outcome != TurnFinished || rec.Status != StatusIdle {
		t.Fatalf("status=%s turn=%+v", rec.Status, got)
	}
}

// A task the Stop lists as settled is no task at all.
func TestSupersededStopIgnoresSettledAndNonShellEntries(t *testing.T) {
	d, id, turnID := startedBridgedTurn(t)
	err := d.ReportSupersededStop(id, supersededStop(t, "s1",
		map[string]any{"id": "b1", "type": "shell", "status": "completed"},
		map[string]any{"id": "m1", "type": "monitor", "status": "running"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := idleRecord(t, d, id).BackgroundTasks; len(got) != 0 {
		t.Fatalf("tracked %+v", got)
	}
	_ = turnID
}

// A superseded Stop that lists an already-tracked task and omits another
// still prunes the omitted one.
func TestSupersededStopReconcilesBothWays(t *testing.T) {
	d, id, _ := startedBridgedTurn(t)
	reportAll(t, d, id, bashStarted(t, "e2", "p-2", "tu-1", "old", "sleep 9"))
	if err := d.ReportSupersededStop(id, supersededStop(t, "s1", runningShell)); err != nil {
		t.Fatal(err)
	}
	if got := trackedIDs(idleRecord(t, d, id)); len(got) != 1 || got[0] != "b1" {
		t.Fatalf("tracked=%v", got)
	}
}
