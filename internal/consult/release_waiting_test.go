package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// startNotifyingWaiting starts an interactive claude dispatch that notifies
// a claude caller, whose opening turn starts background shell b1 and Stops
// with text while it runs: the run is left waiting on 1 shell.
func startNotifyingWaiting(t *testing.T, now *time.Time, text string) (*Dispatcher, *fakeInteractiveRuntime, *fakeNotificationDelivery, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(now)
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", Name: "looper", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	reportAll(t, d, started.ID,
		claudeHook(t, "submit-1", "UserPromptSubmit", dispatchPreamble+" hello"),
		bashStarted(t, "bash-1", "", "tu-1", "b1", "while true; do sleep 5; done"),
		claudeStop(t, "stop-1", text, map[string]any{"background_tasks": []any{map[string]any{"id": "b1", "type": "shell", "status": "running"}}, "session_crons": []any{}}),
	)
	if rec := idleRecord(t, d, started.ID); rec.Status != StatusWaiting || rec.PendingWork.Summary() != "1 shell" {
		t.Fatalf("setup: status=%s pending=%+v", rec.Status, rec.PendingWork)
	}
	return d, rt, delivery, started.ID
}

func slotsUsed(d *Dispatcher) int {
	d.slots.mu.Lock()
	defer d.slots.mu.Unlock()
	return d.slots.used
}

// Releasing a waiting run ends its open turns as an idle release would: a
// turn that said something keeps it as its result, one that said nothing
// ends released. The pane goes, the slot frees, and the caller, who asked
// for this, gets no turn notice.
func TestReleaseWaitingRunFinalizesTurns(t *testing.T) {
	cases := []struct {
		name, text, result string
		outcome            TurnOutcome
	}{
		{"with a result", "the loop is running", "the loop is running", TurnFinished},
		{"without a result", "", releasedNoResultText, TurnReleased},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
			d, rt, delivery, id := startNotifyingWaiting(t, &now, tc.text)
			rec, err := d.Release(id)
			if err != nil {
				t.Fatal(err)
			}
			turn := rec.Turns[0]
			if rec.Status != StatusReleased || turn.Outcome != tc.outcome || turn.Text != tc.result || turn.Pending != nil || turn.SlotHeld {
				t.Fatalf("status=%s turn=%+v", rec.Status, turn)
			}
			if rec.PendingWork != nil || len(rec.BackgroundTasks) != 0 {
				t.Fatalf("pending=%+v tracked=%v", rec.PendingWork, rec.BackgroundTasks)
			}
			if rt.killCount() != 1 || slotsUsed(d) != 0 {
				t.Fatalf("kills=%d slots used=%d", rt.killCount(), slotsUsed(d))
			}
			if n := rec.Notifications[turn.TurnID]; n.Disposition != NotificationSuppressed {
				t.Fatalf("turn notice=%+v", n)
			}
			d.SweepNotifications(context.Background())
			if len(delivery.calls) != 0 {
				t.Fatalf("release notified the caller: %q", delivery.calls)
			}
		})
	}
}

func TestReleaseStillRefusesRunning(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	d, rt, id := startClaudeInteractive(t, &now)
	_, err := d.Release(id)
	if err == nil || !strings.Contains(err.Error(), "cannot be released while running") {
		t.Fatalf("release of a running run: err=%v", err)
	}
	if rec := idleRecord(t, d, id); rec.Status != StatusRunning || rt.killCount() != 0 {
		t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
	}
}
