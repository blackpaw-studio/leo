package consult

func (d *Dispatcher) CloseRecordedPane(rec Record, pane string, kill func(string) error, layout func(string) error) (Record, error) {
	return d.closeRecordedPane(rec, pane, kill, layout)
}

func (d *Dispatcher) PersistViewerRecord(rec Record) {
	d.mu.Lock()
	if state := d.runs[rec.ID]; state != nil {
		state.record.ViewerWindowID = rec.ViewerWindowID
		state.record.ViewerPaneID = rec.ViewerPaneID
		state.record.PaneID = rec.PaneID
		state.record.ViewerKind = rec.ViewerKind
		state.record.ViewerTitle = rec.ViewerTitle
		state.record.CallerWindowID = rec.CallerWindowID
		state.record.CallerPaneID = rec.CallerPaneID
		state.record.CallerSessionID = rec.CallerSessionID
		d.persistLocked(state, "")
		rec = cloneRecord(state.record)
	}
	d.mu.Unlock()
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		_ = recorder.PersistRecord(rec)
	}
}

// closeRecordedPane is the sole pane-kill path. tmux I/O stays outside mu;
// record publication/cleanup and retry metadata are committed under mu.
func (d *Dispatcher) closeRecordedPane(rec Record, pane string, kill func(string) error, layout func(string) error) (Record, error) {
	if pane == "" {
		return rec, nil
	}
	killErr := kill(pane)
	d.mu.Lock()
	state := d.runs[rec.ID]
	if state != nil {
		rec = cloneRecord(state.record)
		paneMatches := rec.PaneID == pane || rec.ViewerPaneID == pane
		terminalWithoutPane := rec.Status.Terminal() && rec.PaneID == "" && rec.ViewerPaneID == ""
		if rec.Status == StatusReleased || terminalWithoutPane || !paneMatches {
			d.mu.Unlock()
			return rec, killErr
		}
	}
	wasSplit := rec.ViewerKind == "split"
	if killErr != nil {
		if rec.Mode == ModeInteractive {
			rec.PaneID = pane
		} else {
			rec.ViewerPaneID = pane
		}
		if state != nil {
			state.record = rec
			state.killPending = true
			d.persistLocked(state, "")
		}
	} else {
		if rec.PaneID == pane {
			rec.PaneID = ""
		}
		if rec.ViewerPaneID == pane {
			rec.ViewerPaneID = ""
		}
		rec.ViewerKind = ""
		if state != nil {
			state.record = rec
			state.killPending = false
			d.persistLocked(state, "")
		}
	}
	d.mu.Unlock()
	if state == nil {
		if recorder, ok := d.recorder.(*FileRecorder); ok {
			_ = recorder.PersistRecord(rec)
		}
	}
	if killErr != nil {
		return rec, killErr
	}
	d.placement.Cancel(rec.ID)
	if wasSplit && rec.CallerWindowID != "" && layout != nil {
		_ = layout(rec.CallerWindowID)
	}
	return rec, nil
}

func runtimeLayout(rt InteractiveRuntime) func(string) error {
	return func(window string) error {
		if layout, ok := rt.(interface{ ReapplyLayout(string) error }); ok {
			return layout.ReapplyLayout(window)
		}
		return nil
	}
}
