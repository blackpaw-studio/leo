package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// startNotifyingClaude starts an interactive claude dispatch that notifies a
// claude caller, finishes its first turn with text, and delivers that turn's
// notice, so what a test then reads is only what the close adds.
func startNotifyingClaude(t *testing.T, now *time.Time, text string) (*Dispatcher, *fakeInteractiveRuntime, *fakeNotificationDelivery, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(now)
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	if err := d.Report(started.ID, claudeHook(t, "submit-1", "UserPromptSubmit", dispatchPreamble+" hello")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, claudeStop(t, "stop-1", text, nil)); err != nil {
		t.Fatal(err)
	}
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 1 {
		t.Fatalf("turn notice calls=%v", delivery.calls)
	}
	delivery.calls = nil
	return d, rt, delivery, started.ID
}

func closeNotice(t *testing.T, d *Dispatcher, id string) Notification {
	t.Helper()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Notifications[id+"#closed"]
}

func requireCloseNotice(t *testing.T, calls []string, id, reason, turnText string) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("calls=%q, want exactly one close notice", calls)
	}
	for _, want := range []string{"closed: " + reason, id + "#1", turnText, closeNoticeTail} {
		if !strings.Contains(calls[0], want) {
			t.Fatalf("notice %q lacks %q", calls[0], want)
		}
	}
}

func TestIdleCloseNotifiesTheCallerOnceWithReasonAndLastTurn(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, _, delivery, id := startNotifyingClaude(t, &now, "background run going")
	now = now.Add(idleCloseAfter)
	d.Sweep(now)
	requireCloseNotice(t, delivery.calls, id, "idle 1h", "background run going")
	if got := closeNotice(t, d, id); got.Disposition != NotificationDelivered {
		t.Fatalf("close notice=%+v", got)
	}
	d.Sweep(now.Add(time.Minute))
	if len(delivery.calls) != 1 {
		t.Fatalf("a later sweep repeated the notice: %q", delivery.calls)
	}
}

func TestClosedPaneNotifiesTheCaller(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, rt, delivery, id := startNotifyingClaude(t, &now, "halfway")
	rt.setAlive(false)
	d.Sweep(now)
	now = now.Add(finalReportGrace)
	d.Sweep(now)
	requireCloseNotice(t, delivery.calls, id, "pane gone", "halfway")
}

func TestGoneCallerSessionNotifiesTheCaller(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, rt, delivery, id := startNotifyingClaude(t, &now, "halfway")
	gone := false
	rt.sessionAlive = &gone
	d.Sweep(now)
	requireCloseNotice(t, delivery.calls, id, "caller session gone", "halfway")
}

func TestEndedHarnessSessionNotifiesTheCaller(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, _, delivery, id := startNotifyingClaude(t, &now, "halfway")
	if err := d.Report(id, claudeHook(t, "end-1", "SessionEnd", "")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(finalReportGrace)
	d.Sweep(now)
	requireCloseNotice(t, delivery.calls, id, "session ended", "halfway")
}

func TestReleaseAndCancelSendNoCloseNotice(t *testing.T) {
	for name, end := range map[string]func(*Dispatcher, string) error{
		"release": func(d *Dispatcher, id string) error { _, err := d.Release(id); return err },
		"cancel":  func(d *Dispatcher, id string) error { _, err := d.Cancel(id); return err },
	} {
		now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
		d, _, delivery, id := startNotifyingClaude(t, &now, "done")
		if err := end(d, id); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		now = now.Add(idleCloseAfter + finalReportGrace)
		d.Sweep(now)
		if _, ok := mustGet(t, d, id).Notifications[id+"#closed"]; ok || len(delivery.calls) != 0 {
			t.Errorf("%s sent a close notice: calls=%q", name, delivery.calls)
		}
	}
}

func mustGet(t *testing.T, d *Dispatcher, id string) Record {
	t.Helper()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestRestartClosedRunNotifiesOnceAcrossReloads(t *testing.T) {
	state := t.TempDir()
	recorder := NewFileRecorder(state)
	h, err := recorder.Open(Record{ID: "d-restart", Kind: "dispatch", Mode: ModeInteractive, Status: StatusIdle, Notify: true, CallerPaneID: "%1", CallerHarness: "claude",
		Turns: []Turn{{TurnID: "d-restart#1", Source: TurnSourceUser, Outcome: TurnFinished, Text: "waiting on the luma run"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(StatusIdle, nil)
	d := NewDispatcher(recorder)
	d.MarkInterrupted()
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	d.SweepNotifications(context.Background())
	calls := delivery.calls
	if len(calls) != 2 {
		t.Fatalf("calls=%q, want the turn notice and the close notice", calls)
	}
	closed := calls[len(calls)-1]
	for _, want := range []string{"closed: daemon restarted", "d-restart#1", "waiting on the luma run", closeNoticeTail} {
		if !strings.Contains(closed, want) {
			t.Fatalf("notice %q lacks %q", closed, want)
		}
	}
	reloaded := NewDispatcher(NewFileRecorder(state))
	reloaded.MarkInterrupted()
	again := &fakeNotificationDelivery{ready: true}
	reloaded.SetNotificationDelivery(again)
	reloaded.SweepNotifications(context.Background())
	if len(again.calls) != 0 {
		t.Fatalf("a reload repeated notices: %q", again.calls)
	}
}
