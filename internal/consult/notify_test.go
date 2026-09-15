package consult

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
