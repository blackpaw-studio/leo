package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// viewerHidden is the ViewerKind of a split pane parked in a window of its
// own, in its caller's session, while its run is idle (see hidePane).
const viewerHidden = "hidden"

// viewerBackground is the ViewerKind of a pane in a window of its own in the
// background session (leo-dispatch), where it sits while the clients attached
// to its root caller's session all ask for background placement (see
// PollPlacement). Unlike "window" it is not in the caller's session.
const viewerBackground = "background"

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
	if _, ok := d.interactiveRuntime.(paneMoverRuntime); !ok || !d.managedLocked(s) {
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

// managedLocked reports whether s's pane is moved to follow its run: a split
// or hidden pane is (between the caller's window and its own), and a pane in
// the background session is. A window-placed one is only when its session
// has gone background, so it can be put there; a pinned one never is.
func (d *Dispatcher) managedLocked(s *runState) bool {
	if s.pinned {
		return false
	}
	_, canBackground := d.interactiveRuntime.(backgroundMoverRuntime)
	switch s.record.ViewerKind {
	case "split", viewerHidden:
		return true
	case viewerBackground:
		return canBackground
	case "window":
		return canBackground && isBackgroundPlacement(s.wantPlacement)
	}
	return false
}

// backgroundWantedLocked reports whether s's session asks for its viewers to
// sit in the background.
func (d *Dispatcher) backgroundWantedLocked(s *runState) bool {
	_, canBackground := d.interactiveRuntime.(backgroundMoverRuntime)
	return canBackground && isBackgroundPlacement(s.wantPlacement)
}

// desiredPlacementLocked is where s's pane should be now. While its session
// is background the pane belongs in the background, whatever the run is
// doing; otherwise an orchestrator turn waiting on a permission decision is
// hidden, and a user-typed turn's permission prompt moves nothing, since the
// user is likely in that pane. Failing those the pane goes where the run's
// latest turn put it (see paneWant). Unmanaged, dying, or releasing panes stay
// as they are.
func (d *Dispatcher) desiredPlacementLocked(s *runState) string {
	actual := s.record.ViewerKind
	if !d.managedLocked(s) || s.record.PaneID == "" || s.killRequested || s.releasing ||
		s.record.Status.Terminal() || s.record.Status == StatusSettling {
		return actual
	}
	userPrompt := false
	if s.record.Status == StatusNeedsInput {
		source, ok := currentTurnSource(s.record)
		userPrompt = ok && source == TurnSourceUser
	}
	switch {
	case userPrompt:
		return actual
	case d.backgroundWantedLocked(s):
		return viewerBackground
	case actual == viewerBackground:
		return d.returnTargetLocked(s)
	case s.record.Status == StatusNeedsInput:
		return viewerHidden
	case s.paneWant == "", s.paneWant == viewerBackground:
		// A user typed a turn into a backgrounded pane: it stays wherever it
		// is now (see buildTurnLocked), it is not sent back.
		return actual
	}
	return s.paneWant
}

// returnTargetLocked is where a pane in the background goes once its session
// is visible again: a window of the caller's session when it asks for window
// placement, hidden when the run has nothing in progress, else back below the
// caller as a split.
func (d *Dispatcher) returnTargetLocked(s *runState) string {
	switch {
	case s.wantPlacement == "window":
		return "window"
	case s.record.Status == StatusNeedsInput, s.paneWant == viewerHidden:
		return viewerHidden
	}
	return "split"
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
		var moved bool
		switch {
		case want == viewerBackground:
			moved = d.backgroundPane(s)
		case actual == viewerBackground:
			moved = d.foregroundPane(s, want)
		case want == viewerHidden:
			moved = d.hidePane(s)
		default:
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

// ownedPane reports whether the run's pane is still where this dispatcher put
// it, for the moves that act on the pane's current place. A runtime that cannot
// say owns every pane; one that can pins a pane the user moved to another
// session (see locateOwnedPane).
func (d *Dispatcher) ownedPane(s *runState) (PaneLocation, bool) {
	d.mu.Lock()
	locator, ok := d.interactiveRuntime.(backgroundMoverRuntime)
	rec := cloneRecord(s.record)
	d.mu.Unlock()
	if !ok {
		return PaneLocation{}, true
	}
	return d.locateOwnedPane(s, locator, rec)
}

// hidePane breaks the run's pane out of the caller's window into a window of
// its own, named like the run's viewer, and records where it went. A pane the
// user moved elsewhere is left where it is.
func (d *Dispatcher) hidePane(s *runState) bool {
	if _, owned := d.ownedPane(s); !owned {
		return false
	}
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
// that is impossible (the caller is gone) the pane stays in its own window,
// which then is simply its window and is no longer moved. So is a pane the
// user moved to another session (it is pinned).
func (d *Dispatcher) showPane(s *runState) bool {
	if _, owned := d.ownedPane(s); !owned {
		return false
	}
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

// currentTurnSource is the source of rec's most recent open turn.
func currentTurnSource(rec Record) (TurnSource, bool) {
	for i := len(rec.Turns) - 1; i >= 0; i-- {
		if rec.Turns[i].Outcome == "" {
			return rec.Turns[i].Source, true
		}
	}
	return "", false
}
