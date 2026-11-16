package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ResolveReleaseOnFinish is a dispatch's release_on_finish setting: the
// explicit value when given, else on for every interactive dispatch, whatever
// its role. On, the pane closes once each result has reached the caller and a
// follow-up reopens it on the same session; an explicit false keeps the pane
// open until the idle close. The role no longer changes the default.
func ResolveReleaseOnFinish(_ string, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return true
}

// closableOnFinishLocked reports whether turnID's result, just delivered
// complete, closes s's pane: the run opted into release_on_finish, turnID is
// its last turn, an orchestrator-sent one that finished, nothing is queued,
// pending or awaiting a decision, and the run is idle (so not needs_input,
// waiting or settling). A turn anyone typed into the pane, at any point,
// means someone is using it. The caller holds d.mu.
func (d *Dispatcher) closableOnFinishLocked(s *runState, turnID string) bool {
	rec := s.record
	if rec.Mode != ModeInteractive || !rec.ReleaseOnFinish || rec.Status != StatusIdle || s.releasing || s.queuedSend != nil || s.awaitingSlot {
		return false
	}
	if len(rec.Turns) == 0 || rec.NeedsInput != nil || len(s.permissions) > 0 || rec.PendingWork != nil {
		return false
	}
	for _, t := range rec.Turns {
		if t.Source == TurnSourceUser {
			return false
		}
	}
	last := rec.Turns[len(rec.Turns)-1]
	return last.TurnID == turnID && last.Source == TurnSourceOrchestrator && last.Outcome == TurnFinished && !last.Queued && last.Pending == nil
}

// parkDeliveredLocked closes run id's pane once turnID's complete result
// reached the caller, keeping the run (its session, cwd and worktree) for a
// follow-up to reopen. The caller holds the run's serial lock and has just
// delivered or returned the result; every other condition is judged here,
// under d.mu, so a state that changed since (a follow-up, a permission
// request) wins. Closing on delivery sends no close notice.
func (d *Dispatcher) parkDeliveredLocked(id, turnID string) error {
	d.mu.Lock()
	s := d.runs[id]
	d.mu.Unlock()
	if s == nil {
		return nil
	}
	admit := func(s *runState) error {
		if !d.closableOnFinishLocked(s, turnID) {
			return errNotClosable
		}
		return nil
	}
	_, err := d.retirePaneLocked(s, nil, admit, func(s *runState) { d.parkLocked(s, turnID) })
	if err == nil || errors.Is(err, errNotClosable) {
		return nil
	}
	return fmt.Errorf("close %s after its result was delivered: %w", id, err)
}

// errNotClosable is a delivered result that does not close its run's pane.
var errNotClosable = &ValidationError{Err: errors.New("not closable on finish")}

// parkLocked commits s as parked (see parked) once its pane is gone. The
// caller holds d.mu.
func (d *Dispatcher) parkLocked(s *runState, turnID string) {
	now := d.now()
	rec := s.record
	rec.Status, rec.Retained = StatusClosed, true
	rec.PaneID, rec.ViewerPaneID, rec.ViewerKind = "", "", ""
	rec.EndedAt = now
	rec.foldActive(now)
	markResultCollected(&rec, turnID, now)
	s.record = rec
	s.killPending = false
	d.persistLocked(s, "status")
	_ = s.handle.Close(StatusClosed, nil)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	d.releaseRunFilesLocked(rec.ID)
}

// markResultCollected records that turnID's complete result reached the
// caller, once.
func markResultCollected(rec *Record, turnID string, now time.Time) bool {
	for i := range rec.Turns {
		if rec.Turns[i].TurnID == turnID && rec.Turns[i].ResultCollectedAt.IsZero() {
			rec.Turns[i].ResultCollectedAt = now
			return true
		}
	}
	return false
}

// noteResultCollectedLocked records on a live interactive run that leo_wait
// returned turnID's complete result, whether or not the run then closes. The
// caller holds the run's serial lock.
func (d *Dispatcher) noteResultCollectedLocked(id, turnID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.runs[id]
	if s == nil || s.record.Mode != ModeInteractive || s.releasing {
		return
	}
	if markResultCollected(&s.record, turnID, d.now()) {
		d.persistLocked(s, "")
	}
}

// releasedSendError explains a send to a released run: why it was released
// and that a follow-up needs a new dispatch.
func releasedSendError(rec Record) error {
	switch {
	case rec.ReleaseReason == ReleaseReasonManual:
		return fmt.Errorf("dispatch %s was released (leo_release); start a new dispatch", rec.ID)
	case rec.ReleaseReason == ReleaseReasonRetention:
		return fmt.Errorf("dispatch %s was released after %s without a follow-up (retention); start a new dispatch", rec.ID, retentionText(resumableRetention))
	case rec.ReleasedOnFinish:
		return fmt.Errorf("dispatch %s was released after finishing (release_on_finish); start a new dispatch", rec.ID)
	}
	return fmt.Errorf("dispatch %s was released; start a new dispatch", rec.ID)
}

// WaitAndClose is Wait followed by deliver, the transport write of the
// entries to the caller. Only once deliver succeeded with ctx still live does
// it record the complete finished results those entries carried as collected
// and close the release_on_finish runs they finished; a failed write or
// canceled request keeps every run as it was. Wait itself never closes.
func (d *Dispatcher) WaitAndClose(ctx context.Context, ids []string, timeout time.Duration, deliver func([]Entry) error) error {
	entries := d.Wait(ctx, ids, timeout)
	if err := deliver(entries); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	d.closeReturned(ids, entries)
	return nil
}

func (d *Dispatcher) closeReturned(ids []string, entries []Entry) {
	unlock := d.serialLocks(ids)
	defer unlock()
	for i, e := range entries {
		if e.Outcome != TurnFinished {
			continue
		}
		runID := strings.SplitN(ids[i], "#", 2)[0]
		turnID := e.TurnID
		if turnID == "" {
			turnID = ids[i]
		}
		d.noteResultCollectedLocked(runID, turnID)
		if e.Status != StatusIdle {
			continue
		}
		if err := d.parkDeliveredLocked(runID, turnID); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: %v\n", runID, err)
		}
	}
}
