package consult

import (
	"context"
	"encoding/json"
	"testing"
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

// turn.complete closes the turn first: the late superseded Stop reconciles
// the tracker only. The run stays idle with its turn finished and nothing is
// resent, since the completion already went out.
func TestSupersededStopAfterTurnCompleteOnlyReconcilesTracker(t *testing.T) {
	d, id, turnID := startedBridgedTurn(t)
	reportAll(t, d, id, bridgeStop(t, "e2", "t2", "the check is running"))
	before := idleRecord(t, d, id)
	if before.Status != StatusIdle {
		t.Fatalf("turn.complete with no pending work should close idle, status=%s", before.Status)
	}
	if err := d.ReportSupersededStop(id, supersededStop(t, "s1", runningShell)); err != nil {
		t.Fatal(err)
	}
	rec := idleRecord(t, d, id)
	got := turnByID(rec, turnID)
	if rec.Status != StatusIdle || got.Outcome != TurnFinished || got.Text != "the check is running" || rec.PendingWork != nil {
		t.Fatalf("late Stop changed the run: status=%s turn=%+v pending=%v", rec.Status, got, rec.PendingWork)
	}
	if !got.EndedAt.Equal(turnByID(before, turnID).EndedAt) || len(rec.Notifications) != len(before.Notifications) {
		t.Fatalf("late Stop reopened or renotified: turn=%+v notifications=%v", got, rec.Notifications)
	}
	if ids := trackedIDs(rec); len(ids) != 1 || ids[0] != "b1" {
		t.Fatalf("tracker not reconciled: %v", ids)
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
