package consult

import (
	"errors"
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
)

// recordPlacement records that the run's pane now sits as kind (in window,
// when that is known), unless the run has moved on to another pane.
func (d *Dispatcher) recordPlacement(s *runState, pane, kind, window string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.PaneID != pane {
		return false
	}
	s.record.ViewerKind = kind
	if window != "" || kind == "split" {
		s.record.ViewerWindowID = window
	}
	d.persistLocked(s, "")
	return true
}

// recordRejoin records that the run's pane is a split below its caller's pane,
// which is now in the given session and window: the pane cap and the layout
// read those from the record, which dates from the dispatch request.
func (d *Dispatcher) recordRejoin(s *runState, pane, callerSession, callerWindow string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.PaneID != pane {
		return false
	}
	s.record.ViewerKind, s.record.ViewerWindowID = "split", ""
	s.record.CallerSessionID, s.record.CallerWindowID = callerSession, callerWindow
	d.persistLocked(s, "")
	return true
}

// locateOwnedPane reads where the run's pane lives. A pane the user parked in
// a session other than its caller's or the background one, or linked into
// several, is pinned and reported not owned.
func (d *Dispatcher) locateOwnedPane(s *runState, mover backgroundMoverRuntime, rec Record) (PaneLocation, bool) {
	loc, err := mover.PaneLocation(d.daemonCtx, rec.PaneID)
	if errors.Is(err, ErrPaneAmbiguous) {
		d.pinViewer(s, rec.PaneID, "it is linked into more than one session")
		return loc, false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: locating viewer pane: %v\n", rec.ID, err)
		return loc, false
	}
	root := d.rootCallerSession(rec)
	if loc.SessionName != dispatchViewerSession && loc.SessionID != root && loc.SessionID != rec.CallerSessionID {
		d.pinViewer(s, rec.PaneID, fmt.Sprintf("it was moved to session %s", loc.SessionName))
		return loc, false
	}
	return loc, true
}

// backgroundPane moves the run's pane into a window of its own in the
// background session and records where it went. A split pane is broken out of
// its caller's window, which is then re-tiled.
func (d *Dispatcher) backgroundPane(s *runState) bool {
	d.mu.Lock()
	mover, _ := d.interactiveRuntime.(backgroundMoverRuntime)
	rec := cloneRecord(s.record)
	layout := runtimeLayout(d.interactiveRuntime)
	d.mu.Unlock()
	loc, owned := d.locateOwnedPane(s, mover, rec)
	if !owned {
		return false
	}
	window, err := mover.BackgroundPane(d.daemonCtx, rec.PaneID, viewerWindowName(rec), loc)
	if errors.Is(err, ErrPaneMoved) {
		return true // probed again on the next step (see reconcilePane)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: moving viewer to the background: %v\n", rec.ID, err)
		return false
	}
	if !d.recordPlacement(s, rec.PaneID, viewerBackground, window) {
		return false
	}
	if rec.ViewerKind == "split" && rec.CallerWindowID != "" {
		_ = layout(rec.CallerWindowID)
	}
	return true
}

// foregroundPane brings a backgrounded pane back to its root caller's
// session as want: below its caller as a split when it can, else in a window
// of its own (hidden, when the run has nothing in progress). A pane whose
// window `leo dispatch attach` has linked comes back by moving that window
// whole and is not hidden: joining it would empty the window and end the
// attach, and a later rejoin from hidden would too.
func (d *Dispatcher) foregroundPane(s *runState, want string) bool {
	d.mu.Lock()
	mover, _ := d.interactiveRuntime.(backgroundMoverRuntime)
	rt := d.interactiveRuntime
	rec := cloneRecord(s.record)
	cfg := d.placementCfg
	d.mu.Unlock()
	loc, owned := d.locateOwnedPane(s, mover, rec)
	if !owned {
		return false
	}
	if loc.SessionName != dispatchViewerSession {
		// Someone already took it out of the background by hand.
		kind := "window"
		if loc.WindowPanes > 1 {
			kind = "split"
		}
		return d.recordPlacement(s, rec.PaneID, kind, loc.WindowID)
	}
	root := d.rootCallerSession(rec)
	if want == "split" && cfg != nil && !loc.WatchLinked {
		switch d.joinBelowCaller(s, rt, rec, loc, root, cfg) {
		case joinDone, joinPaneMoved:
			return true
		}
	}
	window, err := mover.ForegroundPane(d.daemonCtx, rec.PaneID, viewerWindowName(rec), root, loc)
	if errors.Is(err, ErrPaneMoved) {
		return true // probed again on the next step (see reconcilePane)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: returning viewer from the background: %v\n", rec.ID, err)
		return false
	}
	kind := "window"
	if want == viewerHidden && !loc.WatchLinked {
		kind = viewerHidden
	}
	return d.recordPlacement(s, rec.PaneID, kind, window)
}

// joinOutcome is how joinBelowCaller ended.
type joinOutcome int

const (
	// joinSkipped: the pane was not joined, and comes back as a window instead.
	joinSkipped joinOutcome = iota
	// joinDone: the pane is a split below its caller again.
	joinDone
	// joinPaneMoved: the pane was no longer where it was probed (or its window
	// had been linked), so tmux did not join it. Probe it again.
	joinPaneMoved
)

// joinBelowCaller returns the pane, probed at from, below its caller's pane, as
// a launch in pane placement would. It does not (joinSkipped) when the caller's
// pane is gone or no longer in the root caller's session (a nested viewer
// whose parent has not come back), or when the placement coordinator says the
// caller's window is full: the pane then comes back as a window instead. The
// caller's coordinates are read live, since the recorded ones date from when
// the dispatch was requested.
func (d *Dispatcher) joinBelowCaller(s *runState, rt InteractiveRuntime, rec Record, from PaneLocation, root string, cfg *config.Config) joinOutcome {
	mover, _ := rt.(backgroundMoverRuntime)
	shower, _ := rt.(paneMoverRuntime)
	if mover == nil || shower == nil || rec.CallerPaneID == "" {
		return joinSkipped
	}
	callerLoc, err := mover.PaneLocation(d.daemonCtx, rec.CallerPaneID)
	if err != nil || callerLoc.SessionID != root {
		return joinSkipped
	}
	rec.CallerSessionID, rec.CallerWindowID = callerLoc.SessionID, callerLoc.WindowID
	overrides := ViewerOverrides{}
	if provider, ok := rt.(viewerOverridesRuntime); ok {
		overrides = provider.ViewerOverrides(d.daemonCtx, root)
	}
	overrides.Placement = "pane"
	placement := d.placement.Decide(rec, overrides, cfg, d.Records)
	if placement.Kind != "split" {
		return joinSkipped
	}
	if err := shower.ShowPane(d.daemonCtx, rec.PaneID, placement.Target, rec.CallerWindowID, from); err != nil {
		d.placement.Cancel(rec.ID)
		if errors.Is(err, ErrPaneMoved) {
			return joinPaneMoved
		}
		fmt.Fprintf(os.Stderr, "dispatch %s: rejoining viewer below its caller: %v\n", rec.ID, err)
		return joinSkipped
	}
	if !d.placement.Publish(rec.ID, func() bool {
		return d.recordRejoin(s, rec.PaneID, rec.CallerSessionID, rec.CallerWindowID)
	}) {
		return joinSkipped
	}
	return joinDone
}
