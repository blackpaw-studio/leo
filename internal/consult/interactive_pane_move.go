package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// viewerHidden is the ViewerKind of a split pane parked in a background
// window of its own while its run is idle (see hidePane).
const viewerHidden = "hidden"

// paneMoverRuntime moves a live dispatch pane between its caller's window
// and a background window. tmux pane ids (%N) are stable across both moves,
// so every other runtime call keeps addressing the pane by Record.PaneID.
type paneMoverRuntime interface {
	HidePane(ctx context.Context, pane, name string) (windowID string, err error)
	ShowPane(ctx context.Context, pane, targetPane, window string) error
}

// scheduleHideLocked queues a hide once an orchestrator turn has left the
// run idle. The hide itself decides, when it runs, whether to move anything.
func (d *Dispatcher) scheduleHideLocked(s *runState) {
	if s.record.ViewerKind != "split" || s.record.PaneID == "" || s.releasing {
		return
	}
	if _, ok := d.interactiveRuntime.(paneMoverRuntime); !ok {
		return
	}
	s.hidesPending++
	d.enqueuePaneOpLocked(s, paneOpHide, func() { d.hidePane(s) })
}

// scheduleShowLocked queues a rejoin of a hidden pane.
func (d *Dispatcher) scheduleShowLocked(s *runState) <-chan struct{} {
	return d.enqueuePaneOpLocked(s, paneOpShow, func() { d.showPane(s) })
}

// hidePane breaks the run's pane out of the caller's window into a
// background window named like the run's viewer, if the run is still idle
// on a split pane. It runs as a pane op: a turn that starts meanwhile
// queues its own rejoin behind it (see appendTurnLocked and Send).
func (d *Dispatcher) hidePane(s *runState) {
	d.mu.Lock()
	mover, ok := d.interactiveRuntime.(paneMoverRuntime)
	pane := s.record.PaneID
	if !ok || s.releasing || s.killRequested || s.record.Status != StatusIdle || pane == "" || s.record.ViewerKind != "split" {
		s.hidesPending--
		d.mu.Unlock()
		return
	}
	id, name, callerWindow := s.record.ID, viewerWindowName(s.record), s.record.CallerWindowID
	layout := runtimeLayout(d.interactiveRuntime)
	d.mu.Unlock()
	window, err := mover.HidePane(d.daemonCtx, pane, name)
	d.mu.Lock()
	// From here a turn typed in the pane was typed in its background window.
	s.hidesPending--
	if err != nil {
		d.mu.Unlock()
		fmt.Fprintf(os.Stderr, "dispatch %s: hiding idle pane: %v\n", id, err)
		return
	}
	if s.record.PaneID == pane {
		s.record.ViewerKind, s.record.ViewerWindowID = viewerHidden, window
		d.persistLocked(s, "")
	}
	d.mu.Unlock()
	if callerWindow != "" {
		_ = layout(callerWindow)
	}
}

// showPane returns a hidden pane below its caller, where it launched, if
// the run has work in it now: a queued or running turn. An idle run, one
// waiting on the orchestrator's permission decision, or one being torn down
// stays where it is. When rejoining is impossible (the caller is gone) the
// pane stays in its background window, which then is simply its window.
func (d *Dispatcher) showPane(s *runState) {
	d.mu.Lock()
	mover, ok := d.interactiveRuntime.(paneMoverRuntime)
	pane, status := s.record.PaneID, s.record.Status
	if !ok || pane == "" || s.record.ViewerKind != viewerHidden || s.killRequested || s.releasing || (status != StatusQueued && status != StatusRunning) {
		d.mu.Unlock()
		return
	}
	id, target, window := s.record.ID, s.record.CallerPaneID, s.record.CallerWindowID
	d.mu.Unlock()
	err := errors.New("no caller pane recorded")
	if target != "" {
		err = mover.ShowPane(d.daemonCtx, pane, target, window)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.PaneID != pane || s.record.ViewerKind != viewerHidden {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: leaving pane in its own window: %v\n", id, err)
		s.record.ViewerKind = "window"
	} else {
		s.record.ViewerKind, s.record.ViewerWindowID = "split", ""
	}
	d.persistLocked(s, "")
}
