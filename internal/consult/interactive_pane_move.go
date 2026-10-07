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

// scheduleHideLocked parks a split pane once an orchestrator turn has left
// its run idle. The tmux work runs off the dispatcher lock.
func (d *Dispatcher) scheduleHideLocked(s *runState) {
	if s.record.ViewerKind != "split" || s.record.PaneID == "" || s.releasing {
		return
	}
	if _, ok := d.interactiveRuntime.(paneMoverRuntime); !ok {
		return
	}
	go d.hidePane(s, s.record.ID, s.record.PaneID)
}

// hidePane breaks pane out of the caller's window into a background window
// named like the run's viewer. It is serialized with Send, so a follow-up
// either sees the pane hidden (and rejoins it) or finds the run no longer
// idle here.
func (d *Dispatcher) hidePane(s *runState, id, pane string) {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	d.mu.Lock()
	mover, ok := d.interactiveRuntime.(paneMoverRuntime)
	if !ok || s.releasing || s.record.Status != StatusIdle || s.record.PaneID != pane || s.record.ViewerKind != "split" {
		d.mu.Unlock()
		return
	}
	name, callerWindow := viewerWindowName(s.record), s.record.CallerWindowID
	layout := runtimeLayout(d.interactiveRuntime)
	d.mu.Unlock()
	window, err := mover.HidePane(d.daemonCtx, pane, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: hiding idle pane: %v\n", id, err)
		return
	}
	d.mu.Lock()
	if s.record.PaneID != pane {
		d.mu.Unlock()
		return
	}
	s.record.ViewerKind, s.record.ViewerWindowID = viewerHidden, window
	d.persistLocked(s, "")
	// Hook reports are not serialized with this: a turn the user started
	// while the pane was moving finds it gone, so it goes straight back.
	stillIdle := s.record.Status == StatusIdle
	d.mu.Unlock()
	if callerWindow != "" {
		_ = layout(callerWindow)
	}
	if !stillIdle {
		d.showPane(d.daemonCtx, s, pane)
	}
}

// showPane returns a hidden pane below its caller, where it launched. When
// that is impossible (the caller is gone) the pane stays in its background
// window, which then is simply its window.
func (d *Dispatcher) showPane(ctx context.Context, s *runState, pane string) {
	d.mu.Lock()
	mover, ok := d.interactiveRuntime.(paneMoverRuntime)
	if !ok || s.record.ViewerKind != viewerHidden || s.record.PaneID != pane {
		d.mu.Unlock()
		return
	}
	id, target, window := s.record.ID, s.record.CallerPaneID, s.record.CallerWindowID
	d.mu.Unlock()
	err := errors.New("no caller pane recorded")
	if target != "" {
		err = mover.ShowPane(ctx, pane, target, window)
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
