package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestResolveReleaseOnFinishDefaults(t *testing.T) {
	cases := []struct {
		name     string
		role     string
		explicit *bool
		want     bool
	}{
		{"explore defaults on", "explore", nil, true},
		{"plan defaults on", "plan", nil, true},
		{"review defaults on", "review", nil, true},
		{"review subrole defaults on", "review.security", nil, true},
		{"implement defaults off", "implement", nil, false},
		{"implement.hard defaults off", "implement.hard", nil, false},
		{"no role defaults off", "", nil, false},
		{"unknown role defaults off", "reviewer-ish", nil, false},
		{"explicit false wins on a read-only role", "explore", boolPtr(false), false},
		{"explicit true wins on implement", "implement", boolPtr(true), true},
		{"explicit true wins without a role", "", boolPtr(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveReleaseOnFinish(c.role, c.explicit); got != c.want {
				t.Fatalf("ResolveReleaseOnFinish(%q, %v) = %v, want %v", c.role, c.explicit, got, c.want)
			}
		})
	}
}

func TestStartRecordsResolvedReleaseOnFinish(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want bool
	}{
		{"read-only role", Request{Role: "explore"}, true},
		{"implement role", Request{Role: "implement"}, false},
		{"template escape hatch", Request{}, false},
		{"explicit opt-out", Request{Role: "review", ReleaseOnFinish: boolPtr(false)}, false},
		{"explicit opt-in", Request{Role: "implement", ReleaseOnFinish: boolPtr(true)}, true},
		{"headless is a no-op", Request{Role: "explore", Mode: ModeHeadless}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDispatcher(newFakeRecorder())
			d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
			req := c.req
			req.Template, req.Prompt, req.Cwd = "claude", "q", t.TempDir()
			if req.Mode == "" {
				req.Mode = ModeInteractive
			}
			started, err := d.Start(context.Background(), testConfig(), req)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := d.Get(started.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.ReleaseOnFinish != c.want {
				t.Fatalf("record release_on_finish = %v, want %v", rec.ReleaseOnFinish, c.want)
			}
		})
	}
}

// finishedRun is an idle interactive run whose first turn ended with
// outcome and whose completion notification (key d-rof#1) is pending for a
// caller of callerHarness.
func finishedRun(t *testing.T, callerHarness string, outcome TurnOutcome, flag bool) (*Dispatcher, *fakeInteractiveRuntime, *runState) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	rt := &fakeInteractiveRuntime{alive: true}
	d.SetInteractiveRuntime(rt)
	s := &runState{record: Record{
		ID: "d-rof", Kind: "dispatch", Mode: ModeInteractive, Status: StatusIdle, PaneID: "%9", ViewerKind: "split",
		Notify: true, CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1", CallerHarness: callerHarness,
		ReleaseOnFinish: flag, StartedAt: now,
		Turns:         []Turn{{TurnID: "d-rof#1", Source: TurnSourceOrchestrator, Delivered: true, Outcome: outcome, Text: "the answer"}},
		Notifications: map[string]Notification{"d-rof#1": {Disposition: NotificationPending, PendingAt: now, Message: "line"}},
	}, handle: &durableTestHandle{}, done: make(chan struct{})}
	d.runs["d-rof"] = s
	return d, rt, s
}

func status(d *Dispatcher, id string) Record {
	rec, _ := d.Get(id)
	return rec
}

func TestReleaseOnFinishReleasesOnlyAfterInlineDelivery(t *testing.T) {
	d, rt, _ := finishedRun(t, "claude", TurnFinished, true)
	delivery := &fakeNotificationDelivery{ready: false}
	d.SetNotificationDelivery(delivery)

	d.SweepNotifications(context.Background())
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("released before the result reached the caller: status=%s kills=%d", rec.Status, rt.killCount())
	}

	delivery.ready = true
	d.SweepNotifications(context.Background())
	rec := status(d, "d-rof")
	if rec.Status != StatusReleased || !rec.ReleasedOnFinish || rec.PaneID != "" || rt.killCount() != 1 {
		t.Fatalf("after delivery: status=%s released_on_finish=%v pane=%q kills=%d", rec.Status, rec.ReleasedOnFinish, rec.PaneID, rt.killCount())
	}
	if rec.Notifications["d-rof#1"].Disposition != NotificationDelivered {
		t.Fatalf("notification = %+v", rec.Notifications["d-rof#1"])
	}
}

func TestReleaseOnFinishDoesNotReleaseOnFailedDelivery(t *testing.T) {
	d, rt, _ := finishedRun(t, "claude", TurnFinished, true)
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true, err: ErrNotificationAmbiguous})
	d.SweepNotifications(context.Background())
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("released on a failed delivery: %s kills=%d", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishPointerOnlyDeliveryWaitsForCollection(t *testing.T) {
	d, rt, _ := finishedRun(t, "codex", TurnFinished, true)
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("a pointer-only notification does not carry the result, yet released: %s", rec.Status)
	}
	entry := d.Wait(context.Background(), []string{"d-rof"}, 0)[0]
	if entry.Text != "the answer" || entry.Outcome != TurnFinished {
		t.Fatalf("wait entry = %+v", entry)
	}
	if rec := status(d, "d-rof"); rec.Status != StatusReleased || !rec.ReleasedOnFinish || rt.killCount() != 1 {
		t.Fatalf("after leo_wait returned the result: %s kills=%d", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishWaitReleasesWhenNotifyIsOff(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	s.record.Notify = false
	entry := d.Wait(context.Background(), []string{"d-rof"}, 0)[0]
	if entry.Text != "the answer" {
		t.Fatalf("the wait must still return the result, got %+v", entry)
	}
	if rec := status(d, "d-rof"); rec.Status != StatusReleased || rt.killCount() != 1 {
		t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishNotifyOffWithNoCollectorFallsBackToIdleClose(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	s.record.Notify = false
	s.record.Notifications["d-rof#1"] = Notification{Disposition: NotificationSuppressed, SuppressedAt: time.Unix(1000, 0)}
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 0 {
		t.Fatalf("a suppressed notification was delivered: %v", delivery.calls)
	}
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("nothing collected the result, yet %s (kills=%d)", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishOutputSnapshotReleases(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	s.record.Notify = false
	if err := d.ReleaseDelivered("d-rof"); err != nil {
		t.Fatal(err)
	}
	if rec := status(d, "d-rof"); rec.Status != StatusReleased || rt.killCount() != 1 {
		t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishSkipsRunsThatAreNotCleanlyFinished(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *runState)
		out   TurnOutcome
		flag  bool
	}{
		{"flag off", func(*runState) {}, TurnFinished, false},
		{"interrupted", func(*runState) {}, TurnInterrupted, true},
		{"lost", func(*runState) {}, TurnLost, true},
		{"rejected", func(*runState) {}, TurnRejected, true},
		{"needs input", func(s *runState) {
			s.record.Status = StatusNeedsInput
			s.record.NeedsInput = &NeedsInput{RequestID: "r1", Tool: "Bash"}
		}, TurnFinished, true},
		{"follow-up queued for a slot", func(s *runState) {
			s.record.Turns = append(s.record.Turns, Turn{TurnID: "d-rof#2", Source: TurnSourceOrchestrator, Queued: true})
			s.record.Status = StatusQueued
			s.queuedSend = &queuedSend{turnID: "d-rof#2"}
		}, TurnFinished, true},
		{"follow-up running", func(s *runState) {
			s.record.Turns = append(s.record.Turns, Turn{TurnID: "d-rof#2", Source: TurnSourceOrchestrator, Delivered: true})
			s.record.Status = StatusRunning
		}, TurnFinished, true},
		{"user typed a second turn", func(s *runState) {
			s.record.Turns = append(s.record.Turns, Turn{TurnID: "d-rof#2", Source: TurnSourceUser, Outcome: TurnFinished, Text: "hi"})
		}, TurnFinished, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, rt, s := finishedRun(t, "claude", c.out, c.flag)
			c.setup(s)
			d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
			d.SweepNotifications(context.Background())
			d.Wait(context.Background(), []string{"d-rof"}, time.Millisecond)
			if err := d.ReleaseDelivered("d-rof"); err != nil {
				t.Fatal(err)
			}
			if rec := status(d, "d-rof"); rec.Status == StatusReleased || rec.ReleasedOnFinish || rt.killCount() != 0 {
				t.Fatalf("released: status=%s kills=%d", rec.Status, rt.killCount())
			}
		})
	}
}

func TestSendToARunReleasedOnFinishNamesWhy(t *testing.T) {
	d, rt, _ := finishedRun(t, "claude", TurnFinished, true)
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())

	_, err := d.Send(context.Background(), "d-rof", "one more thing")
	want := "dispatch d-rof was released after finishing (release_on_finish); start a new dispatch"
	if err == nil || err.Error() != want {
		t.Fatalf("send error = %v, want %q", err, want)
	}
	if rt.injectionCount() != 0 {
		t.Fatalf("a message was injected into a released run's pane")
	}
}

func TestSendToARestoredReleasedOnFinishRunNamesWhy(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	rec := Record{ID: "d-rof", Kind: "dispatch", Mode: ModeInteractive, Status: StatusReleased, ReleaseOnFinish: true, ReleasedOnFinish: true}
	if _, err := recorder.Open(rec); err != nil {
		t.Fatal(err)
	}
	if err := recorder.PersistRecord(rec); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(NewFileRecorder(stateDir))
	_, err := d.Send(context.Background(), "d-rof", "more")
	if err == nil || !strings.Contains(err.Error(), "released after finishing (release_on_finish)") {
		t.Fatalf("error = %v", err)
	}
}

func TestReleaseOnFinishReleasesWhenTheBridgeAcksTheNotification(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	n := s.record.Notifications["d-rof#1"]
	n.Disposition, n.Transport, n.DeliveredAt = NotificationDelivered, NotificationTransportBridge, time.Unix(1000, 0)
	s.record.Notifications["d-rof#1"] = n
	d.collectAcked(cloneRecord(s.record), "d-rof#1")
	if rec := status(d, "d-rof"); rec.Status != StatusReleased || !rec.ReleasedOnFinish || rt.killCount() != 1 {
		t.Fatalf("status=%s released_on_finish=%v kills=%d", rec.Status, rec.ReleasedOnFinish, rt.killCount())
	}
}

func TestManualReleaseDoesNotClaimReleaseOnFinish(t *testing.T) {
	d, _, _ := finishedRun(t, "claude", TurnFinished, true)
	rec, err := d.Release("d-rof")
	if err != nil || rec.Status != StatusReleased || rec.ReleasedOnFinish {
		t.Fatalf("manual release = %+v, %v", rec, err)
	}
	if _, err := d.Send(context.Background(), "d-rof", "x"); err == nil || strings.Contains(err.Error(), "release_on_finish") {
		t.Fatalf("a manual release keeps its generic error, got %v", err)
	}
}
