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

// maxReconcileSteps bounds one reconcile's moves, so state that keeps
// flipping cannot spin the worker; the next nudge picks it up again.
const maxReconcileSteps = 8

// nudgePaneLocked asks s's pane-op worker to reconcile the pane's placement
// with the run's state, coalescing with a reconcile already queued. The
// returned channel closes once that reconcile has run.
func (d *Dispatcher) nudgePaneLocked(s *runState) <-chan struct{} {
	if _, ok := d.interactiveRuntime.(paneMoverRuntime); !ok || !managedPlacement(s.record.ViewerKind) {
		done := make(chan struct{})
		close(done)
		return done
	}
	if s.reconcileQueued != nil {
		return s.reconcileQueued
	}
	s.reconcileQueued = d.enqueuePaneOpLocked(s, paneOpReconcile, func() { d.reconcilePane(s) })
	return s.reconcileQueued
}

// managedPlacement reports whether a pane with this ViewerKind is moved
// between the caller's window and a background one: a split pane is, a
// window-placed one (or one whose rejoin failed) never is.
func managedPlacement(kind string) bool {
	return kind == "split" || kind == viewerHidden
}

// desiredPlacementLocked is where s's pane should be now. A pane waiting on
// the orchestrator's permission decision is hidden; otherwise it goes where
// the run's latest turn put it (see paneWant). Unmanaged, dying, or
// releasing panes stay as they are.
func (d *Dispatcher) desiredPlacementLocked(s *runState) string {
	actual := s.record.ViewerKind
	if !managedPlacement(actual) || s.record.PaneID == "" || s.killRequested || s.releasing ||
		s.record.Status.Terminal() || s.record.Status == StatusSettling {
		return actual
	}
	if s.record.Status == StatusNeedsInput {
		return viewerHidden
	}
	if s.paneWant == "" {
		return actual
	}
	return s.paneWant
}

// reconcilePane moves s's pane, one tmux step at a time, until its recorded
// placement matches the desired one, re-reading the run's state before each
// step. A failed step stops it; the next nudge tries again.
func (d *Dispatcher) reconcilePane(s *runState) {
	d.mu.Lock()
	s.reconcileQueued = nil
	d.mu.Unlock()
	for range maxReconcileSteps {
		d.mu.Lock()
		want, actual := d.desiredPlacementLocked(s), s.record.ViewerKind
		d.mu.Unlock()
		if want == actual {
			return
		}
		moved := false
		if want == viewerHidden {
			moved = d.hidePane(s)
		} else {
			moved = d.showPane(s)
		}
		if !moved {
			return
		}
	}
	d.mu.Lock()
	fmt.Fprintf(os.Stderr, "dispatch %s: pane placement still changing after %d moves\n", s.record.ID, maxReconcileSteps)
	d.mu.Unlock()
}

// hidePane breaks the run's pane out of the caller's window into a
// background window named like the run's viewer, and records where it went.
func (d *Dispatcher) hidePane(s *runState) bool {
	d.mu.Lock()
	mover, _ := d.interactiveRuntime.(paneMoverRuntime)
	id, pane, name, callerWindow := s.record.ID, s.record.PaneID, viewerWindowName(s.record), s.record.CallerWindowID
	layout := runtimeLayout(d.interactiveRuntime)
	d.mu.Unlock()
	window, err := mover.HidePane(d.daemonCtx, pane, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: hiding idle pane: %v\n", id, err)
		return false
	}
	d.mu.Lock()
	if s.record.PaneID == pane {
		s.record.ViewerKind, s.record.ViewerWindowID = viewerHidden, window
		d.persistLocked(s, "")
	}
	d.mu.Unlock()
	if callerWindow != "" {
		_ = layout(callerWindow)
	}
	return true
}

// showPane returns a hidden pane below its caller, where it launched. When
// that is impossible (the caller is gone) the pane stays in its background
// window, which then is simply its window and is no longer moved.
func (d *Dispatcher) showPane(s *runState) bool {
	d.mu.Lock()
	mover, _ := d.interactiveRuntime.(paneMoverRuntime)
	id, pane, target, window := s.record.ID, s.record.PaneID, s.record.CallerPaneID, s.record.CallerWindowID
	d.mu.Unlock()
	err := errors.New("no caller pane recorded")
	if target != "" {
		err = mover.ShowPane(d.daemonCtx, pane, target, window)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.PaneID != pane {
		return false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: leaving pane in its own window: %v\n", id, err)
		s.record.ViewerKind = "window"
		d.persistLocked(s, "")
		return false
	}
	s.record.ViewerKind, s.record.ViewerWindowID = "split", ""
	d.persistLocked(s, "")
	return true
}
