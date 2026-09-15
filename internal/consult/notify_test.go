package consult

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inspectDelivery struct {
	ready   bool
	deliver func(Record, string) error
}

func (f inspectDelivery) Ready(context.Context, Record) bool { return f.ready }
func (f inspectDelivery) Deliver(_ context.Context, r Record, line string) error {
	return f.deliver(r, line)
}

type failingRecordHandle struct{ nopHandle }

func (failingRecordHandle) SetRecord(Record) error { return errors.New("disk full") }

type failingRecorder struct{ h failingRecordHandle }

func (r failingRecorder) Open(Record) (Handle, error) { return r.h, nil }

type fakeNotificationDelivery struct {
	ready bool
	err   error
	calls []string
}

func (f *fakeNotificationDelivery) Ready(context.Context, Record) bool { return f.ready }
func (f *fakeNotificationDelivery) Deliver(_ context.Context, r Record, line string) error {
	f.calls = append(f.calls, r.CallerPaneID+"\x00"+line)
	return f.err
}

func TestCompletionNotificationExactLineAndSanitizes(t *testing.T) {
	r := Record{ID: "d-1", Name: "bad\n\x1bname", Template: "worker", Status: StatusDone, ActiveSeconds: 65.9}
	got := completionNotification(r, "", StatusDone)
	want := "[leo] dispatch d-1 (bad name) done · active 1:05 — collect with leo_wait"
	if got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestCompletionCandidateSuppressesCoveredWaitAndDeduplicates(t *testing.T) {
	d := NewDispatcher(nil)
	now := time.Unix(10, 0)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusDone}, handle: nopHandle{}}
	d.mu.Lock()
	d.waits["d-x"] = 2
	d.completionCandidateLocked(s, "d-x", StatusDone)
	d.completionCandidateLocked(s, "d-x", StatusDone)
	d.mu.Unlock()
	n := s.record.Notifications["d-x"]
	if len(s.record.Notifications) != 1 || n.Disposition != NotificationSuppressed || !n.SuppressedAt.Equal(now) {
		t.Fatalf("notification = %+v", n)
	}
}

func TestSweepNotificationsPendingBusyThenClaimsBeforeDelivery(t *testing.T) {
	d := NewDispatcher(nil)
	f := &fakeNotificationDelivery{}
	d.SetNotificationDelivery(f)
	now := time.Unix(20, 0)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: nopHandle{}}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	if len(f.calls) != 0 || s.record.Notifications["d-x"].Disposition != NotificationPending {
		t.Fatal("busy delivery must remain pending")
	}
	f.ready = true
	d.SweepNotifications(context.Background())
	if len(f.calls) != 1 || s.record.Notifications["d-x"].Disposition != NotificationDelivered {
		t.Fatalf("calls=%v ledger=%+v", f.calls, s.record.Notifications)
	}
	d.SweepNotifications(context.Background())
	if len(f.calls) != 1 {
		t.Fatal("delivered notification repeated")
	}
}

func TestSweepNotificationsCoveredAfterCompletionSuppresses(t *testing.T) {
	d := NewDispatcher(nil)
	f := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(f)
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: nopHandle{}}
	d.runs["d-x"] = s
	d.waits["d-x"] = 1
	d.SweepNotifications(context.Background())
	if len(f.calls) != 0 || s.record.Notifications["d-x"].Disposition != NotificationSuppressed {
		t.Fatal("covered pending notification delivered")
	}
}

func TestSweepNotificationsClaimedAndAmbiguousFailuresNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial NotificationDisposition
		err     error
	}{
		{"restart-claimed", NotificationClaimed, nil}, {"ambiguous", NotificationPending, ErrNotificationAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(nil)
			f := &fakeNotificationDelivery{ready: true, err: tc.err}
			d.SetNotificationDelivery(f)
			s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: tc.initial}}}, handle: nopHandle{}}
			d.runs["d-x"] = s
			d.SweepNotifications(context.Background())
			d.SweepNotifications(context.Background())
			wantCalls := 1
			if tc.initial == NotificationClaimed {
				wantCalls = 0
			}
			if len(f.calls) != wantCalls {
				t.Fatalf("calls=%d", len(f.calls))
			}
			if tc.err != nil && s.record.Notifications["d-x"].Disposition != NotificationFailed {
				t.Fatal("ambiguous failure not final")
			}
		})
	}
}

func TestNotificationNoSendFailureReturnsPending(t *testing.T) {
	d := NewDispatcher(nil)
	f := &fakeNotificationDelivery{ready: true, err: ErrNotificationNotSent}
	d.SetNotificationDelivery(f)
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: nopHandle{}}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	if !errors.Is(f.err, ErrNotificationNotSent) || s.record.Notifications["d-x"].Disposition != NotificationPending {
		t.Fatal("proven no-send should retry")
	}
	if strings.Count(f.calls[0], "[leo] dispatch") != 1 {
		t.Fatal("missing line")
	}
}

func TestNotificationClaimPersistsAfterClosedFileHandleBeforeDelivery(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	d := NewDispatcher(recorder)
	h, err := recorder.Open(Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusRunning}, handle: h, done: make(chan struct{})}
	d.runs["d-x"] = s
	d.complete(s, StatusDone, "done", nil)
	d.SetNotificationDelivery(inspectDelivery{ready: true, deliver: func(_ Record, _ string) error {
		rec, err := LoadOne(filepath.Dir(recorder.dir), "d-x")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Notifications["d-x"].Disposition != NotificationClaimed {
			t.Fatalf("disk disposition=%s", rec.Notifications["d-x"].Disposition)
		}
		return nil
	}})
	d.SweepNotifications(context.Background())
	rec, err := LoadOne(stateDir, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Notifications["d-x"].Disposition != NotificationDelivered {
		t.Fatalf("disk disposition=%s", rec.Notifications["d-x"].Disposition)
	}
}

func TestNotificationClaimPersistenceFailurePreventsIO(t *testing.T) {
	d := NewDispatcher(failingRecorder{})
	calls := 0
	d.SetNotificationDelivery(inspectDelivery{ready: true, deliver: func(Record, string) error { calls++; return nil }})
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: failingRecordHandle{}}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	if calls != 0 {
		t.Fatal("delivery occurred after failed claim persistence")
	}
	if s.record.Notifications["d-x"].Disposition != NotificationPending {
		t.Fatal("failed claim did not remain pending")
	}
}

func TestHeadlessCancelCreatesCompletionCandidate(t *testing.T) {
	d := NewDispatcher(nil)
	ctx, cancel := context.WithCancel(context.Background())
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusRunning}, handle: nopHandle{}, cancel: cancel, done: make(chan struct{})}
	d.runs["d-x"] = s
	d.terminateState(s, StatusCanceled)
	if s.record.Notifications["d-x"].Disposition != NotificationPending {
		t.Fatalf("notifications=%+v", s.record.Notifications)
	}
	_ = ctx
}

func TestRegisterWaitSuppressesExistingPendingCandidate(t *testing.T) {
	d := NewDispatcher(nil)
	s := &runState{record: Record{ID: "d-x", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: nopHandle{}, done: make(chan struct{})}
	close(s.done)
	d.runs["d-x"] = s
	entries := d.Wait(context.Background(), []string{"d-x"}, time.Second)
	if len(entries) != 1 || s.record.Notifications["d-x"].Disposition != NotificationSuppressed {
		t.Fatalf("entries=%+v notifications=%+v", entries, s.record.Notifications)
	}
}

func TestTurnCandidatePersistsBoundaryMessage(t *testing.T) {
	d := NewDispatcher(nil)
	now := time.Unix(100, 0)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-x", Name: "job", Notify: true, CallerPaneID: "%1", Mode: ModeInteractive, Status: StatusRunning, ActiveSeconds: 65, Turns: []Turn{{TurnID: "d-x#1"}}}, handle: nopHandle{}}
	d.mu.Lock()
	d.closeTurnLocked(s, "d-x#1", TurnFinished, "")
	first := s.record.Notifications["d-x#1"].Message
	s.record.ActiveSeconds = 999
	s.record.Status = StatusTimeout
	d.mu.Unlock()
	if first != "[leo] dispatch d-x#1 (job) done · active 1:05 — collect with leo_wait" {
		t.Fatalf("message=%q", first)
	}
}

func TestPruneKeepsPendingNotificationsResident(t *testing.T) {
	d := NewDispatcher(nil)
	for i := 0; i < RecordsKept+2; i++ {
		id := fmt.Sprintf("d-%02d", i)
		d.runs[id] = &runState{record: Record{ID: id, Status: StatusDone, EndedAt: time.Unix(int64(i), 0)}, handle: nopHandle{}}
	}
	d.runs["d-pending"] = &runState{record: Record{ID: "d-pending", Status: StatusDone, EndedAt: time.Unix(0, 0), Notifications: map[string]Notification{"d-pending": {Disposition: NotificationPending}}}, handle: nopHandle{}}
	d.mu.Lock()
	d.pruneTerminalRunsLocked()
	d.mu.Unlock()
	if d.runs["d-pending"] == nil {
		t.Fatal("pending notification was pruned")
	}
}
