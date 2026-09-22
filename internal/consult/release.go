package consult

import "fmt"

func (d *Dispatcher) Release(id string) (Record, error) {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	return d.releaseLocked(id, nil)
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
	switch rec.Status {
	case StatusIdle, StatusDone, StatusFailed, StatusCanceled, StatusTimeout, StatusClosed:
	default:
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
		switch rec.Status {
		case StatusIdle, StatusDone, StatusFailed, StatusCanceled, StatusTimeout, StatusClosed:
		default:
			d.mu.Unlock()
			return rec, invalidf("dispatch %s cannot be released while %s", id, rec.Status)
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
		rec, closeErr = d.closeRecordedPane(rec, rec.PaneID, rt.Kill, layout)
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
	return rec, nil
}
