package consult

import (
	"errors"
	"fmt"
	"time"
)

// Release ends interactive run id for good: its pane is killed, its
// retained worktree goes through cleanup under the usual safeguards, and a
// later follow-up is rejected.
func (d *Dispatcher) Release(id string) (Record, error) {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	return d.releaseLocked(id, nil, ReleaseReasonManual)
}

// releasable reports whether a run in status may be released: one with no
// turn at work. A waiting run qualifies, since its background work may never
// wake it; its open turns end on release (see endWaitingTurnsForReleaseLocked).
func releasable(status Status) bool {
	switch status {
	case StatusIdle, StatusWaiting, StatusDone, StatusFailed, StatusCanceled, StatusTimeout, StatusClosed:
		return true
	}
	return false
}

// releasedNoResultText is the result of a turn its run was released out from
// under before it said anything.
const releasedNoResultText = "released while waiting on background work, before the turn produced a result"

// endWaitingTurnsForReleaseLocked ends every open turn of a waiting run its
// caller is releasing, leaving the run idle for the release to finish: a turn
// that said something before it paused keeps that as its result and
// finishes; any other ends released. The caller asked for this, so no turn
// notice goes out. The background work dies with the pane.
func (d *Dispatcher) endWaitingTurnsForReleaseLocked(s *runState) {
	boundary := d.now()
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome != "" {
			continue
		}
		t.Outcome = TurnFinished
		closing := ""
		if len(t.WaitText) == 0 {
			t.Outcome, closing = TurnReleased, releasedNoResultText
		}
		t.mergeWaitText(closing)
		t.Pending, t.PendingTaskIDs = nil, nil
		t.EndedAt = boundary
		if t.SlotHeld {
			t.SlotHeld = false
			d.slotsFor(s).Release()
		}
		if t.Queued {
			t.Queued = false
			d.dequeueSendLocked(s, t.TurnID)
		}
		if s.armedTurn == t.TurnID {
			s.armedTurn, s.armedUntil = "", time.Time{}
		}
		d.recordTurnNoticeLocked(s, t.TurnID, turnNotificationStatus(s.record, t.TurnID), true)
		d.persistTurnLocked(s, *t)
	}
	s.record.BackgroundTasks = nil
	s.adoptWaits = nil
	d.recomputeStatusLocked(s, boundary)
	s.record.foldActive(boundary)
	d.persistLocked(s, "status")
}

// releaseLocked releases run id for reason (ReleaseReasonManual or
// ReleaseReasonRetention). The caller holds the run's serial lock.
func (d *Dispatcher) releaseLocked(id string, layout func(string) error, reason string) (Record, error) {
	rec, state, err := d.lookup(id)
	if err != nil {
		return Record{}, fmt.Errorf("unknown dispatch %s", id)
	}
	if rec.Mode != ModeInteractive {
		return rec, invalidf("dispatch %s is not interactive", id)
	}
	if rec.Status == StatusReleased {
		return rec, nil
	}
	if !releasable(rec.Status) {
		return rec, invalidf("dispatch %s cannot be released while %s", id, rec.Status)
	}
	if state != nil {
		admit := func(s *runState) error {
			if !releasable(s.record.Status) {
				return invalidf("dispatch %s cannot be released while %s", id, s.record.Status)
			}
			if s.record.Status == StatusWaiting {
				d.endWaitingTurnsForReleaseLocked(s)
			}
			return nil
		}
		rec, err = d.retirePaneLocked(state, layout, admit, func(s *runState) { d.commitReleaseLocked(s, reason) })
		if err != nil {
			if errors.As(err, new(*ValidationError)) {
				return rec, err
			}
			return rec, fmt.Errorf("release dispatch %s: %w", id, err)
		}
		if fr, ok := d.recorder.(*FileRecorder); ok {
			_ = fr.PersistRecord(rec)
		}
	} else {
		d.mu.Lock()
		rt := d.interactiveRuntime
		d.mu.Unlock()
		if rec.PaneID != "" && rt != nil {
			if layout == nil {
				layout = runtimeLayout(rt)
			}
			var closeErr error
			if rec, closeErr = d.closeRecordedPane(rec, rec.PaneID, rt.Kill, layout); closeErr != nil {
				return rec, fmt.Errorf("release dispatch %s: %w", id, closeErr)
			}
		}
		rec.Status, rec.PaneID, rec.ViewerPaneID, rec.ViewerKind = StatusReleased, "", "", ""
		rec.Retained, rec.ReleaseReason = false, reason
		if rec.EndedAt.IsZero() {
			rec.EndedAt = d.now()
		}
		if d.recorder != nil {
			h, openErr := d.recorder.Resume(rec)
			if openErr != nil {
				return rec, openErr
			}
			if rh, ok := h.(recordHandle); ok {
				if err := rh.SetRecord(rec); err != nil {
					return rec, err
				}
			}
			_ = h.Close(StatusReleased, nil)
		}
	}
	// Released runs never launch again, in memory or restored from disk.
	d.mu.Lock()
	d.releaseRunFilesLocked(id)
	d.mu.Unlock()
	// The pane is gone, so a cleanup deferred (or kept) while it was alive,
	// or held for a parked run's follow-up, can finally be decided. Dirty,
	// unmerged or unverifiable work is kept.
	if rec.Isolation == "worktree" {
		rec = d.cleanupWorktree(id)
	}
	return rec, nil
}

// commitReleaseLocked marks s released for reason once its pane is gone; a
// parked run stops being retained, so its worktree is cleaned up. The caller
// holds d.mu.
func (d *Dispatcher) commitReleaseLocked(s *runState, reason string) {
	current := s.record
	current.Status, current.PaneID, current.ViewerPaneID, current.ViewerKind = StatusReleased, "", "", ""
	current.Retained, current.ReleaseReason = false, reason
	if current.EndedAt.IsZero() {
		current.EndedAt = d.now()
	}
	current.foldActive(current.EndedAt)
	s.record = current
	s.killPending = false
	d.persistLocked(s, "status")
	_ = s.handle.Close(StatusReleased, nil)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// retirePaneLocked takes run s's pane down, then commits what that means for
// the run: a release (commitReleaseLocked) or closing on a delivered result
// (parkLocked). admit runs under d.mu first and may refuse (refusals are
// ValidationErrors; anything else is the kill's error); from then until
// commit, s.releasing keeps reports, sweeps and follow-ups off the run. The
// kill is ordered with the run's other pane ops and kills whatever pane the
// run has when it runs. A failed kill commits nothing. The caller holds the
// run's serial lock.
func (d *Dispatcher) retirePaneLocked(s *runState, layout func(string) error, admit func(*runState) error, commit func(*runState)) (Record, error) {
	d.mu.Lock()
	if s.releasing {
		rec := cloneRecord(s.record)
		d.mu.Unlock()
		return rec, invalidf("dispatch %s is being released", rec.ID)
	}
	if admit != nil {
		if err := admit(s); err != nil {
			rec := cloneRecord(s.record)
			d.mu.Unlock()
			return rec, err
		}
	}
	s.releasing = true
	rt := d.interactiveRuntime
	rec := cloneRecord(s.record)
	d.mu.Unlock()
	defer func() { d.mu.Lock(); s.releasing = false; d.mu.Unlock() }()
	if rec.PaneID != "" && rt != nil {
		if layout == nil {
			layout = runtimeLayout(rt)
		}
		var closeErr error
		d.mu.Lock()
		closed := d.enqueuePaneOpLocked(s, paneOpKill, func() {
			d.mu.Lock()
			current := cloneRecord(s.record)
			d.mu.Unlock()
			rec, closeErr = d.closeRecordedPane(current, current.PaneID, rt.Kill, layout)
		})
		d.mu.Unlock()
		<-closed
		if closeErr != nil {
			return rec, closeErr
		}
	}
	d.mu.Lock()
	commit(s)
	rec = cloneRecord(s.record)
	d.mu.Unlock()
	return rec, nil
}
