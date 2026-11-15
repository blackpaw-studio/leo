package consult

import (
	"fmt"
	"os"
	"time"
)

// confirmReopenLocked records that the harness resumed the run's own
// session: from here it is an ordinary live run, which closes and cleans up
// as one unless its next result parks it again.
func (d *Dispatcher) confirmReopenLocked(s *runState) {
	s.reopenUnconfirmed = false
	s.record.Retained = false
	d.persistLocked(s, "")
}

// failReopenLocked handles a reopen whose harness reported a session other
// than the run's: it started fresh, which a reopen never does. The pane is
// killed, the follow-up rejected, and the run parked again as no longer
// resumable. The run keeps its own SessionID.
func (d *Dispatcher) failReopenLocked(s *runState, started string) {
	reason := fmt.Sprintf("could not resume session %s (harness started %s)", s.record.SessionID, started)
	d.parkFailedReopenLocked(s, TurnRejected, reason, true)
	if rt := d.interactiveRuntime; rt != nil && s.killPending && !d.paneOpQueuedLocked(s, paneOpKill) {
		d.enqueuePaneOpLocked(s, paneOpKill, func() { d.retryPaneKill(s, rt) })
	}
}

// revertReopenLocked parks a reopen that ended with status before its
// session was confirmed. Only its pane exiting on its own says the session
// cannot be resumed; a cancel, timeout, launch failure or caller going away
// leaves it resumable.
func (d *Dispatcher) revertReopenLocked(s *runState, status Status) {
	switch {
	case status == StatusCanceled || status == StatusTimeout:
		d.parkFailedReopenLocked(s, TurnInterrupted, fmt.Sprintf("the reopen was %s before session %s resumed", status, s.record.SessionID), false)
	case status == StatusClosed && (s.closeReason == closeReasonPaneGone || s.closeReason == closeReasonSessionEnded):
		d.parkFailedReopenLocked(s, TurnRejected, fmt.Sprintf("the reopened pane exited before it resumed session %s", s.record.SessionID), true)
	default:
		d.parkFailedReopenLocked(s, TurnRejected, fmt.Sprintf("the reopen ended (%s) before session %s resumed", status, s.record.SessionID), false)
	}
}

// failedReopenText is the closing text of a failed reopen's turn.
func failedReopenText(id, reason string, permanent bool) string {
	next := "send the follow-up again to retry, or leo_release it"
	if permanent {
		next = "it is no longer resumable; leo_release it and start a new dispatch"
	}
	return fmt.Sprintf("%s; nothing was continued. Dispatch %s is parked again: %s.", reason, id, next)
}

// parkFailedReopenLocked returns an unconfirmed reopen to parked: its open
// turns close with outcome and reason, and a permanent failure records
// reason as the run's ResumeError. No close notice is owed and the
// worktree stays; a pane still standing is left to the kill retry
// (killPending), which keeps a parked run's worktree.
func (d *Dispatcher) parkFailedReopenLocked(s *runState, outcome TurnOutcome, reason string, permanent bool) {
	if permanent {
		s.record.ResumeError = reason
	}
	text := failedReopenText(s.record.ID, reason, permanent)
	d.dropPermissionsLocked(s)
	clearPendingLocked(s)
	s.record.BackgroundTasks, s.record.OpenTools = nil, nil
	for _, t := range s.record.Turns {
		if t.Outcome == "" {
			d.closeTurnLocked(s, t.TurnID, outcome, text)
		}
	}
	now := d.now()
	s.reopenUnconfirmed, s.awaitingSlot = false, false
	s.record.foldActive(now)
	s.record.Status, s.record.EndedAt, s.record.Retained = StatusClosed, now, true
	s.killPending = s.record.PaneID != ""
	d.persistLocked(s, "status")
	d.releaseRunFilesLocked(s.record.ID)
	_ = s.handle.Close(StatusClosed, nil)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	fmt.Fprintf(os.Stderr, "dispatch %s: reopen failed, parked again: %s\n", s.record.ID, reason)
}

// awaitPreviousPaneGone makes sure no earlier pane of parked run rec still
// runs before a reopen resumes its session: two harnesses must never run one
// session side by side. A pane a failed reopen or a restart left recorded
// (its kill pending) is killed now, through the run's pane-op queue so the
// kill follows any already queued, and the reopen is refused while the pane
// is still there. A refusal leaves the kill pending, never discarded. It
// returns the record as the kill left it. The caller holds the run's serial
// lock.
func (d *Dispatcher) awaitPreviousPaneGone(rec Record, rt InteractiveRuntime) (Record, error) {
	if rec.PaneID == "" {
		return rec, nil
	}
	d.mu.Lock()
	tracked := d.runs[rec.ID] != nil
	d.mu.Unlock()
	if !tracked {
		d.trackRestartKill(rec)
	}
	d.mu.Lock()
	s := d.runs[rec.ID]
	s.killPending = true
	done := d.enqueuePaneOpLocked(s, paneOpKill, func() { d.retryPaneKill(s, rt) })
	d.mu.Unlock()
	d.awaitPaneOp(done)
	got, _, err := d.lookup(rec.ID)
	if err != nil {
		return Record{}, err
	}
	if got.PaneID != "" {
		return Record{}, fmt.Errorf("dispatch %s's previous pane (%s) is still closing; send the follow-up again shortly", rec.ID, got.PaneID)
	}
	return got, nil
}

// parkRestartedReopen parks a reopen a restart found still waiting for its
// slot: it never launched, so nothing was continued and it stays resumable.
// Its turn's notification is owed; no close notice is.
func (d *Dispatcher) parkRestartedReopen(rec Record) int {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return 0
	}
	markedAt := d.now()
	text := failedReopenText(rec.ID, fmt.Sprintf("the daemon restarted before session %s resumed", rec.SessionID), false)
	for i := range rec.Turns {
		if rec.Turns[i].Outcome == "" {
			t := &rec.Turns[i]
			t.Outcome, t.Text, t.EndedAt = TurnInterrupted, text, markedAt
			t.SlotHeld, t.Queued, t.Pending, t.PendingTaskIDs = false, false, nil, nil
		}
	}
	rec.PendingWork, rec.BackgroundTasks, rec.OpenTools, rec.NeedsInput = nil, nil, nil, nil
	rec.Status, rec.EndedAt = StatusClosed, markedAt
	rec.foldActive(markedAt)
	d.mu.Lock()
	d.addRestartTurnCandidates(&rec)
	d.mu.Unlock()
	return d.persistRestartSettle(recorder, rec)
}

// timeoutStart is when a run's timeout counts from: its start, or its
// latest reopen.
func timeoutStart(s *runState) time.Time {
	if s.reopenedAt.After(s.record.StartedAt) {
		return s.reopenedAt
	}
	return s.record.StartedAt
}
