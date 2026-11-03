package consult

import (
	"fmt"
	"time"
)

func (d *Dispatcher) Release(id string) (Record, error) {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	return d.releaseLocked(id, nil)
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

func (d *Dispatcher) releaseLocked(id string, layout func(string) error) (Record, error) {
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
	d.mu.Lock()
	rt := d.interactiveRuntime
	if state != nil {
		rec = cloneRecord(state.record)
		if state.releasing {
			d.mu.Unlock()
			return rec, invalidf("dispatch %s is being released", id)
		}
		if !releasable(rec.Status) {
			d.mu.Unlock()
			return rec, invalidf("dispatch %s cannot be released while %s", id, rec.Status)
		}
		if rec.Status == StatusWaiting {
			d.endWaitingTurnsForReleaseLocked(state)
			rec = cloneRecord(state.record)
		}
		state.releasing = true
	}
	d.mu.Unlock()
	if state != nil {
		defer func() { d.mu.Lock(); state.releasing = false; d.mu.Unlock() }()
	}
	if rec.PaneID != "" && rt != nil {
		var closeErr error
		if layout == nil {
			layout = runtimeLayout(rt)
		}
		if state != nil {
			// Ordered with the run's other pane ops, and killing whatever
			// pane the run has when it runs.
			d.mu.Lock()
			closed := d.enqueuePaneOpLocked(state, paneOpKill, func() {
				d.mu.Lock()
				current := cloneRecord(state.record)
				d.mu.Unlock()
				rec, closeErr = d.closeRecordedPane(current, current.PaneID, rt.Kill, layout)
			})
			d.mu.Unlock()
			<-closed
		} else {
			rec, closeErr = d.closeRecordedPane(rec, rec.PaneID, rt.Kill, layout)
		}
		if closeErr != nil {
			return rec, fmt.Errorf("release dispatch %s: %w", id, closeErr)
		}
	}
	rec.Status, rec.PaneID, rec.ViewerPaneID, rec.ViewerKind = StatusReleased, "", "", ""
	if rec.EndedAt.IsZero() {
		rec.EndedAt = d.now()
	}
	if state != nil {
		d.mu.Lock()
		current := state.record
		current.Status, current.PaneID, current.ViewerPaneID, current.ViewerKind = StatusReleased, "", "", ""
		current.ReleasedOnFinish = state.releasingOnFinish
		if current.EndedAt.IsZero() {
			current.EndedAt = d.now()
		}
		current.foldActive(current.EndedAt)
		state.record = current
		state.killPending = false
		d.persistLocked(state, "status")
		_ = state.handle.Close(StatusReleased, nil)
		select {
		case <-state.done:
		default:
			close(state.done)
		}
		rec = cloneRecord(state.record)
		d.mu.Unlock()
		if fr, ok := d.recorder.(*FileRecorder); ok {
			_ = fr.PersistRecord(rec)
		}
	} else if d.recorder != nil {
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
	// Released runs never launch again, in memory or restored from disk.
	d.mu.Lock()
	d.releaseRunFilesLocked(id)
	d.mu.Unlock()
	// The pane is gone, so a cleanup deferred (or kept) while it was alive
	// can finally be decided. This is the path manual release, close and
	// release_on_finish all take.
	if rec.Isolation == "worktree" {
		rec = d.cleanupWorktree(id)
	}
	return rec, nil
}
