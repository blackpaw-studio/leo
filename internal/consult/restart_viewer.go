package consult

func (d *Dispatcher) reconcileRestartViewer(rec Record) Record {
	if rec.ViewerKind != "split" || rec.CallerWindowID == "" || rec.PaneID != "" || rec.ViewerPaneID != "" {
		return rec
	}
	finder, ok := d.interactiveRuntime.(paneDispatchRuntime)
	if !ok {
		return rec
	}
	pane, err := finder.FindPaneByDispatchID(rec.CallerWindowID, rec.ID)
	if err != nil {
		return rec
	}
	switch {
	case pane == "":
		rec.ViewerKind, rec.ViewerTitle = "", ""
	case rec.Mode == ModeInteractive:
		rec.PaneID = pane
	default:
		rec.ViewerPaneID = pane
	}
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		_ = recorder.PersistRecord(rec)
	}
	return rec
}
