package consult

import (
	"testing"
	"time"
)

// A run's status is derived from its open turns in one place: running while
// any turn actively works, else waiting while any waits on background work,
// else queued while a sent turn has not started, else idle.

// waitingRun is an interactive run with one turn waiting on background work.
func waitingRun(t *testing.T) (*Dispatcher, *runState, string, *time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(&now)
	s := &runState{record: Record{ID: "d-wait", Harness: "claude", Mode: ModeInteractive, Status: StatusRunning, StartedAt: now}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs[s.record.ID] = s
	id := d.openTurnLocked(s, TurnSourceUser, "", false).TurnID
	d.bindKeyLocked(s, id, "p:a")
	d.waitOnBackgroundLocked(s, pendingWorkFromStop(shellAndMonitor), &s.record.Turns[0])
	if s.record.Status != StatusWaiting {
		t.Fatalf("setup status = %s", s.record.Status)
	}
	return d, s, id, &now
}

func TestStatusStaysWaitingWhenASentTurnIsOpenedBesideAWaitingOne(t *testing.T) {
	d, s, _, _ := waitingRun(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.appendTurnLocked(s, TurnSourceOrchestrator, "next", false)
	if s.record.Status != StatusWaiting || s.record.PendingWork == nil {
		t.Fatalf("opening a sent turn rewrote the wait: status=%s pending=%v", s.record.Status, s.record.PendingWork)
	}
}

func TestStatusRunsWhileADeliveredTurnWorksBesideAWaitingOneThenWaitsAgain(t *testing.T) {
	d, s, _, _ := waitingRun(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	sentID := d.appendTurnLocked(s, TurnSourceOrchestrator, "next", false).TurnID
	d.deliverTurnLocked(s, sentID, "b")
	if s.record.Status != StatusRunning {
		t.Fatalf("delivered turn works: status=%s", s.record.Status)
	}
	d.closeTurnLocked(s, sentID, TurnFinished, "done")
	if s.record.Status != StatusWaiting || s.record.PendingWork == nil {
		t.Fatalf("the wait did not resume once the other turn ended: status=%s pending=%v", s.record.Status, s.record.PendingWork)
	}
}

// A run that begins settling holds no wait: not in its turns, not on the run.
func TestSettlingClearsEveryTurnsPendingWork(t *testing.T) {
	d, s, id, _ := waitingRun(t)
	d.mu.Lock()
	d.beginSettlementLocked(s, StatusCanceled, time.Minute)
	got := turnByID(s.record, id).Pending
	run := s.record.PendingWork
	d.mu.Unlock()
	if got != nil || run != nil {
		t.Fatalf("a settling run still waits: turn=%v run=%v", got, run)
	}
}

// A restart ends every open turn lost; none keeps a wait.
func TestMarkInterruptedClearsPendingWork(t *testing.T) {
	state := t.TempDir()
	fr := NewFileRecorder(state)
	work := &PendingWork{Tasks: map[string]int{"shell": 1}}
	h, err := fr.Open(Record{ID: "d-wait", Mode: ModeInteractive, Status: StatusWaiting, StartedAt: time.Now(), PendingWork: work, Turns: []Turn{{TurnID: "d-wait#1", Source: TurnSourceUser, Pending: work}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetStatus(StatusWaiting); err != nil {
		t.Fatal(err)
	}
	NewDispatcher(fr).MarkInterrupted()
	rec, err := LoadOne(state, "d-wait")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status.Terminal() == false || rec.PendingWork != nil || rec.Turns[0].Pending != nil || rec.Turns[0].Outcome != TurnLost {
		t.Fatalf("a restarted run still waits: %+v", rec)
	}
}

// A waiting turn that has been quiet for the waiting threshold is stalled
// though a newer turn has since finished.
func TestStalledLooksAtEveryOpenTurn(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	rec := callerRecord("d1", "alpha", StatusWaiting)
	rec.Mode = ModeInteractive
	work := &PendingWork{Tasks: map[string]int{"shell": 1}}
	rec.PendingWork = work
	rec.HookActivity = now.Add(-waitingStalledAfter)
	rec.Turns = []Turn{
		{TurnID: "d1#1", Source: TurnSourceUser, StartedAt: now.Add(-3 * time.Hour), Pending: work},
		{TurnID: "d1#2", Source: TurnSourceUser, StartedAt: now.Add(-time.Hour), Outcome: TurnFinished},
	}
	if !isStalled(rec, now) {
		t.Fatal("a finished newer turn hid an older waiting turn quiet past the waiting threshold")
	}
	rec.HookActivity = now.Add(-waitingStalledAfter + time.Minute)
	if isStalled(rec, now) {
		t.Fatal("a waiting turn is not stalled before the waiting threshold")
	}
	if got := interactiveEntry(rec, "d1#1", now); got.Stalled {
		t.Fatal("leo_wait reports a waiting turn stalled before the waiting threshold")
	}
	rec.HookActivity = now.Add(-waitingStalledAfter)
	if got := interactiveEntry(rec, "d1#1", now); !got.Stalled {
		t.Fatal("leo_wait does not report a waiting turn stalled past the waiting threshold")
	}
}
