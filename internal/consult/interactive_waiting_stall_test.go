package consult

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// The 2026-11-03 waiting stall (d-e27f0353134d), replayed: a bridged
// dispatch, whose mod never sees Claude's Stop, ends its opening turn with a
// background shell that never finishes. Child dispatches' notices then
// arrive as turns of their own, and the session goes quiet for hours.

const stallSweepEvery = 5 * time.Minute

// startStalledBridged starts a notifying bridged dispatch whose opening turn
// starts shell b1 and ends, so the PostToolUse is the tracker's only witness.
func startStalledBridged(t *testing.T, now *time.Time) (*Dispatcher, *bridgedFakeRuntime, *fakeNotificationDelivery, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(now)
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", Name: "stalled", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	open := bridge.OpeningID(DispatchBridgeKey(started.ID), requestPrompt(Request{Prompt: "hello"}))
	reportAll(t, d, started.ID,
		bridgeStart(t, "bridge:turn.start:t1", "t1", "hello", open),
		bashStarted(t, "bash-1", "p-1", "tu-1", "b1", "until false; do sleep 5; done"),
		bridgeStop(t, "bridge:turn.complete:t1", "t1", "watching the build"),
	)
	if rec := mustGet(t, d, started.ID); rec.Status != StatusWaiting || rec.PendingWork.Summary() != "1 shell" {
		t.Fatalf("setup: status=%s pending=%+v", rec.Status, rec.PendingWork)
	}
	return d, rt, delivery, started.ID
}

// childTurn is a child dispatch's notice arriving in the pane as a turn of
// its own, with whatever hooks it fires before its turn completes.
func childTurn(t *testing.T, d *Dispatcher, id, turnID string, mid ...HookReport) {
	t.Helper()
	reports := []HookReport{bridgeStart(t, "bridge:turn.start:"+turnID, turnID, "[leo] dispatch d-child done", "notify-d-child-"+turnID)}
	reports = append(reports, mid...)
	reports = append(reports, bridgeStop(t, "bridge:turn.complete:"+turnID, turnID, "read "+turnID))
	reportAll(t, d, id, reports...)
}

func waitingNoticesDelivered(t *testing.T, d *Dispatcher, delivery *fakeNotificationDelivery, id string) int {
	t.Helper()
	n := 0
	for _, call := range delivery.calls {
		if strings.Contains(call, id+" (stalled) waiting on") {
			n++
		}
	}
	return n
}

func TestWaitingStallReplay(t *testing.T) {
	start := time.Date(2026, time.November, 3, 4, 0, 0, 0, time.UTC)
	now := start
	d, _, delivery, id := startStalledBridged(t, &now)
	opening := mustGet(t, d, id).Turns[0].TurnID
	advanceClock(&now, 5*time.Minute)
	childTurn(t, d, id, "t2")
	advanceClock(&now, 5*time.Minute)
	childTurn(t, d, id, "t3")
	quietFrom := now
	for at := quietFrom.Add(stallSweepEvery); !at.After(quietFrom.Add(4 * time.Hour)); at = at.Add(stallSweepEvery) {
		sweepAt(d, &now, at)
		if at.Sub(quietFrom) == waitingStalledAfter && !isStalled(mustGet(t, d, id), at) {
			t.Fatalf("not stalled after %s quiet", waitingStalledAfter)
		}
	}
	if got := waitingNoticesDelivered(t, d, delivery, id); got != 1 {
		t.Fatalf("waiting notices delivered=%d, want 1; calls=%q", got, delivery.calls)
	}
	if rec := mustGet(t, d, id); rec.Status != StatusWaiting {
		t.Fatalf("status=%s", rec.Status)
	}
	if _, err := d.Send(context.Background(), id, "status?"); err == nil || !strings.Contains(err.Error(), "leo_release or leo_cancel") {
		t.Fatalf("send to the stalled run: err=%v", err)
	}
	rec, err := d.Release(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := turnByID(rec, opening); rec.Status != StatusReleased || got.Outcome != TurnFinished || got.Text != "watching the build" {
		t.Fatalf("status=%s opening=%+v", rec.Status, got)
	}
}

// The same run, but its TaskStop on the dead shell is refused as gone: the
// tracker prunes it, the opening turn finishes, and nobody is told it waits.
func TestWaitingStallReplayFailedTaskStopEndsTheWait(t *testing.T) {
	start := time.Date(2026, time.November, 3, 4, 0, 0, 0, time.UTC)
	now := start
	d, _, delivery, id := startStalledBridged(t, &now)
	opening := mustGet(t, d, id).Turns[0].TurnID
	advanceClock(&now, 5*time.Minute)
	childTurn(t, d, id, "t2", idHook(t, "stop-fail", "PostToolUseFailure", "p-2", map[string]any{
		"tool_name": "TaskStop", "tool_use_id": "tu-2", "tool_input": map[string]any{"task_id": "b1"}, "error": "No task found with ID: b1",
	}))
	advanceClock(&now, 5*time.Minute)
	childTurn(t, d, id, "t3")
	rec := mustGet(t, d, id)
	if got := turnByID(rec, opening); rec.Status != StatusIdle || got.Outcome != TurnFinished || got.Text != "watching the build" {
		t.Fatalf("status=%s opening=%+v", rec.Status, got)
	}
	quietFrom := now
	for at := quietFrom.Add(stallSweepEvery); !at.After(quietFrom.Add(4 * time.Hour)); at = at.Add(stallSweepEvery) {
		sweepAt(d, &now, at)
	}
	if got := waitingNoticesDelivered(t, d, delivery, id); got != 0 || len(waitingNotices(t, d, id)) != 0 {
		t.Fatalf("waiting notices delivered=%d calls=%q", got, delivery.calls)
	}
}
