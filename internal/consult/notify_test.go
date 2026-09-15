package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
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

func (r failingRecorder) Open(Record) (Handle, error)   { return r.h, nil }
func (r failingRecorder) Resume(Record) (Handle, error) { return r.h, nil }

type countingFailHandle struct {
	nopHandle
	writes int
}

func (h *countingFailHandle) SetRecord(Record) error { h.writes++; return errors.New("disk full") }

type durableTestHandle struct {
	nopHandle
	rec Record
}

func (h *durableTestHandle) SetRecord(rec Record) error { h.rec = rec; return nil }

type failedRestartKillRuntime struct{}

func (failedRestartKillRuntime) Launch(context.Context, LaunchRequest) (string, string, error) {
	return "", "", errors.New("unused")
}
func (failedRestartKillRuntime) Inject(context.Context, string, string, func() error) error {
	return errors.New("unused")
}
func (failedRestartKillRuntime) Alive(string) bool         { return true }
func (failedRestartKillRuntime) Kill(string) error         { return errors.New("kill failed") }
func (failedRestartKillRuntime) ComposerEmpty(string) bool { return true }

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
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: &durableTestHandle{}}
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
			s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: tc.initial}}}, handle: &durableTestHandle{}}
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
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: &durableTestHandle{}}
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
	h, err := recorder.Open(Record{ID: "d-x", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Status: StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Status: StatusRunning}, handle: h, done: make(chan struct{})}
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
	if s.record.Notifications["d-x"].Disposition != NotificationFailed {
		t.Fatal("failed claim did not fail closed")
	}
}

func TestNondurableHandleFailsClosedWithoutDelivery(t *testing.T) {
	d := NewDispatcher(nil)
	calls := 0
	d.SetNotificationDelivery(inspectDelivery{ready: true, deliver: func(Record, string) error { calls++; return nil }})
	s := &runState{record: Record{ID: "d-x", Status: StatusDone, CallerPaneID: "%1", Notifications: map[string]Notification{"d-x": {Disposition: NotificationPending}}}, handle: nopHandle{}}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	if calls != 0 || s.record.Notifications["d-x"].Disposition != NotificationFailed {
		t.Fatalf("calls=%d notification=%+v", calls, s.record.Notifications["d-x"])
	}
}

func TestHeadlessCancelCreatesCompletionCandidate(t *testing.T) {
	d := NewDispatcher(nil)
	ctx, cancel := context.WithCancel(context.Background())
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Status: StatusRunning}, handle: &durableTestHandle{}, cancel: cancel, done: make(chan struct{})}
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

func TestPruneKeepsResidentWhileWorktreeOrNotificationUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		worktreeKept, pending bool
		wantRetained          bool
	}{
		{name: "both unresolved", worktreeKept: true, pending: true, wantRetained: true},
		{name: "notification only", pending: true, wantRetained: true},
		{name: "worktree only", worktreeKept: true, wantRetained: true},
		{name: "both resolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(nil)
			for i := 0; i < RecordsKept+1; i++ {
				id := fmt.Sprintf("d-new-%02d", i)
				d.runs[id] = &runState{record: Record{ID: id, Status: StatusDone, EndedAt: time.Unix(int64(i+1), 0)}, handle: nopHandle{}}
			}
			rec := Record{ID: "d-oldest", Status: StatusDone, EndedAt: time.Unix(0, 0), WorktreeState: WorktreeRemoved}
			if tc.worktreeKept {
				rec.Isolation, rec.WorktreeState = "worktree", WorktreeKept
			}
			if tc.pending {
				rec.Notifications = map[string]Notification{rec.ID: {Disposition: NotificationPending}}
			}
			d.runs[rec.ID] = &runState{record: rec, handle: nopHandle{}}
			d.mu.Lock()
			d.pruneTerminalRunsLocked()
			d.mu.Unlock()
			_, retained := d.runs[rec.ID]
			if retained != tc.wantRetained {
				t.Fatalf("retained = %v, want %v", retained, tc.wantRetained)
			}
		})
	}
}

func TestPendingNotificationExpiresFailedAfterOneHour(t *testing.T) {
	d := NewDispatcher(nil)
	now := time.Unix(7200, 0)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-old", Status: StatusDone, Notifications: map[string]Notification{"d-old": {Disposition: NotificationPending, PendingAt: now.Add(-time.Hour)}}}, handle: &durableTestHandle{}}
	d.runs["d-old"] = s
	d.SetNotificationDelivery(&fakeNotificationDelivery{})
	d.SweepNotifications(context.Background())
	n := s.record.Notifications["d-old"]
	if n.Disposition != NotificationFailed || !n.FailedAt.Equal(now) {
		t.Fatalf("notification=%+v", n)
	}
}

func TestAbandonedClaimExpiresFailedAfterOneHour(t *testing.T) {
	d := NewDispatcher(nil)
	now := time.Unix(7200, 0)
	d.now = func() time.Time { return now }
	h := &durableTestHandle{}
	s := &runState{record: Record{ID: "d-claimed", Status: StatusDone, Notifications: map[string]Notification{"d-claimed": {Disposition: NotificationClaimed, ClaimedAt: now.Add(-time.Hour)}}}, handle: h}
	d.runs["d-claimed"] = s
	d.SweepNotifications(context.Background())
	if got := s.record.Notifications["d-claimed"].Disposition; got != NotificationFailed {
		t.Fatalf("disposition=%s", got)
	}
}

func TestUnknownCallerHarnessSuppressesCandidate(t *testing.T) {
	d := NewDispatcher(nil)
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", Status: StatusDone}, handle: nopHandle{}}
	d.mu.Lock()
	d.completionCandidateLocked(s, "d-x", StatusDone)
	d.mu.Unlock()
	if got := s.record.Notifications["d-x"].Disposition; got != NotificationSuppressed {
		t.Fatalf("disposition=%s", got)
	}
}

func TestCandidatePersistenceFailureFailsOnce(t *testing.T) {
	d := NewDispatcher(nil)
	h := &countingFailHandle{}
	s := &runState{record: Record{ID: "d-x", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Status: StatusDone}, handle: h}
	d.mu.Lock()
	d.completionCandidateLocked(s, "d-x", StatusDone)
	d.completionCandidateLocked(s, "d-x", StatusDone)
	d.mu.Unlock()
	if got := s.record.Notifications["d-x"].Disposition; got != NotificationFailed || h.writes != 1 {
		t.Fatalf("disposition=%s writes=%d", got, h.writes)
	}
}

func TestCompleteCandidatePersistenceFailureWritesOnce(t *testing.T) {
	d := NewDispatcher(nil)
	h := &countingFailHandle{}
	s := &runState{record: Record{ID: "d-complete", Kind: "dispatch", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Status: StatusRunning}, handle: h, done: make(chan struct{})}
	d.complete(s, StatusDone, "done", nil)
	if got := s.record.Notifications["d-complete"].Disposition; got != NotificationFailed || h.writes != 1 {
		t.Fatalf("disposition=%s writes=%d", got, h.writes)
	}
}

func TestClaimedExpiryPersistenceFailureStaysFailedOnce(t *testing.T) {
	now := time.Unix(7200, 0)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	h := &countingFailHandle{}
	s := &runState{record: Record{ID: "d-x", Status: StatusDone, Notifications: map[string]Notification{"d-x": {Disposition: NotificationClaimed, ClaimedAt: now.Add(-time.Hour)}}}, handle: h}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	d.SweepNotifications(context.Background())
	if got := s.record.Notifications["d-x"].Disposition; got != NotificationFailed || h.writes != 1 {
		t.Fatalf("disposition=%s writes=%d", got, h.writes)
	}
}

func TestOverlappingTurnCandidateSnapshotsLiveActiveAtBoundary(t *testing.T) {
	d := NewDispatcher(nil)
	start := time.Unix(100, 0)
	boundary := start.Add(5 * time.Second)
	d.now = func() time.Time { return boundary }
	s := &runState{record: Record{ID: "d-x", Name: "job", Notify: true, CallerPaneID: "%1", Mode: ModeInteractive, Status: StatusRunning, RunningSince: &start, Turns: []Turn{{TurnID: "d-x#1", Delivered: true}, {TurnID: "d-x#2", Delivered: true}}}, handle: nopHandle{}}
	d.mu.Lock()
	d.closeTurnLocked(s, "d-x#1", TurnFinished, "")
	got := s.record.Notifications["d-x#1"].Message
	d.mu.Unlock()
	if !strings.Contains(got, "active 0:05") {
		t.Fatalf("message=%q", got)
	}
}

func TestTimeoutTurnCandidateUsesEffectiveTerminalStatus(t *testing.T) {
	d := NewDispatcher(nil)
	now := time.Unix(100, 0)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-x", Name: "job", Notify: true, CallerPaneID: "%1", Mode: ModeInteractive, Status: StatusRunning, Turns: []Turn{{TurnID: "d-x#1"}}}, handle: nopHandle{}, done: make(chan struct{})}
	d.mu.Lock()
	d.beginSettlementLocked(s, StatusTimeout, 0)
	d.finishInteractiveLocked(s, StatusTimeout)
	got := s.record.Notifications["d-x#1"].Message
	d.mu.Unlock()
	if !strings.Contains(got, ") timeout ·") {
		t.Fatalf("message=%q", got)
	}
}

func TestMarkInterruptedFailedKillInstallsDurableHandleBeforeDelivery(t *testing.T) {
	state := t.TempDir()
	recorder := NewFileRecorder(state)
	now := time.Unix(100, 0)
	recorder.Now = func() time.Time { return now }
	h, err := recorder.Open(Record{ID: "d-restart", Kind: "dispatch", Mode: ModeInteractive, Status: StatusRunning, PaneID: "%2", Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Turns: []Turn{{TurnID: "d-restart#1"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(StatusRunning, nil)
	d := NewDispatcher(recorder)
	d.now = func() time.Time { return now }
	d.SetInteractiveRuntime(failedRestartKillRuntime{})
	d.MarkInterrupted()
	d.SetNotificationDelivery(inspectDelivery{ready: true, deliver: func(_ Record, _ string) error {
		got, err := LoadOne(state, "d-restart")
		if err != nil {
			t.Fatal(err)
		}
		if got.Notifications["d-restart#1"].Disposition != NotificationClaimed {
			t.Fatalf("disk notification=%+v", got.Notifications)
		}
		return nil
	}})
	d.SweepNotifications(context.Background())
}

func TestMarkInterruptedRestoresTerminalIsolatedNotificationAsReaped(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	d := NewDispatcher(recorder)
	rec := Record{
		ID: "d-restored", Kind: "dispatch", Status: StatusDone,
		Isolation: "worktree", WorktreeState: WorktreeKept,
		Notifications: map[string]Notification{"d-restored": {Disposition: NotificationPending}},
	}
	if err := os.MkdirAll(recorder.dir, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(recorder.dir, rec); err != nil {
		t.Fatal(err)
	}

	d.MarkInterrupted()
	d.mu.Lock()
	s := d.runs[rec.ID]
	d.mu.Unlock()
	if s == nil {
		t.Fatal("pending terminal notification was not restored")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("restored terminal run is not marked reaped")
	}
	entries := d.Wait(context.Background(), []string{rec.ID}, 0)
	if len(entries) != 1 || entries[0].Status != StatusDone {
		t.Fatalf("entries=%+v", entries)
	}
	got, err := d.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Notifications[rec.ID]; !ok {
		t.Fatalf("notification lost after collection: %+v", got.Notifications)
	}
}

func TestPruneRetainsClaimedNotificationUntilNoSendRetryResolves(t *testing.T) {
	d := NewDispatcher(nil)
	started := make(chan struct{})
	release := make(chan struct{})
	d.SetNotificationDelivery(inspectDelivery{ready: true, deliver: func(Record, string) error { close(started); <-release; return ErrNotificationNotSent }})
	claimed := &runState{record: Record{ID: "d-claimed", Status: StatusDone, EndedAt: time.Unix(0, 0), CallerPaneID: "%1", Notifications: map[string]Notification{"d-claimed": {Disposition: NotificationPending}}}, handle: &durableTestHandle{}}
	d.runs["d-claimed"] = claimed
	for i := 0; i < RecordsKept+1; i++ {
		id := fmt.Sprintf("d-done-%d", i)
		d.runs[id] = &runState{record: Record{ID: id, Status: StatusDone, EndedAt: time.Unix(int64(i+1), 0)}, handle: nopHandle{}}
	}
	done := make(chan struct{})
	go func() { d.SweepNotifications(context.Background()); close(done) }()
	<-started
	d.mu.Lock()
	d.pruneTerminalRunsLocked()
	d.mu.Unlock()
	if d.runs["d-claimed"] == nil {
		t.Fatal("claimed notification evicted during delivery")
	}
	close(release)
	<-done
	if d.runs["d-claimed"] == nil || claimed.record.Notifications["d-claimed"].Disposition != NotificationPending {
		t.Fatalf("run=%v notification=%+v", d.runs["d-claimed"] != nil, claimed.record.Notifications)
	}
}
