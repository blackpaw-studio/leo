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
	if _, owned := d.locateOwnedPane(s, mover, rec); !owned {
		return false
	}
	window, err := mover.BackgroundPane(d.daemonCtx, rec.PaneID, viewerWindowName(rec))
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
// of its own (hidden, when the run has nothing in progress).
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
	if want == "split" && cfg != nil && d.joinBelowCaller(s, rt, rec, root, cfg) {
		return true
	}
	window, err := mover.ForegroundPane(d.daemonCtx, rec.PaneID, viewerWindowName(rec), root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: returning viewer from the background: %v\n", rec.ID, err)
		return false
	}
	kind := "window"
	if want == viewerHidden {
		kind = viewerHidden
	}
	return d.recordPlacement(s, rec.PaneID, kind, window)
}

// joinBelowCaller returns the pane below its caller's pane, as a launch in pane
// placement would, and reports whether it did. It does not when the caller's
// pane is gone or no longer in the root caller's session (a nested viewer
// whose parent has not come back), or when the placement coordinator says the
// caller's window is full: the pane then comes back as a window instead. The
// caller's coordinates are read live, since the recorded ones date from when
// the dispatch was requested.
func (d *Dispatcher) joinBelowCaller(s *runState, rt InteractiveRuntime, rec Record, root string, cfg *config.Config) bool {
	mover, _ := rt.(backgroundMoverRuntime)
	shower, _ := rt.(paneMoverRuntime)
	if mover == nil || shower == nil || rec.CallerPaneID == "" {
		return false
	}
	callerLoc, err := mover.PaneLocation(d.daemonCtx, rec.CallerPaneID)
	if err != nil || callerLoc.SessionID != root {
		return false
	}
	rec.CallerSessionID, rec.CallerWindowID = callerLoc.SessionID, callerLoc.WindowID
	overrides := ViewerOverrides{}
	if provider, ok := rt.(viewerOverridesRuntime); ok {
		overrides = provider.ViewerOverrides(d.daemonCtx, root)
	}
	overrides.Placement = "pane"
	placement := d.placement.Decide(rec, overrides, cfg, d.Records)
	if placement.Kind != "split" {
		return false
	}
	if err := shower.ShowPane(d.daemonCtx, rec.PaneID, placement.Target, rec.CallerWindowID); err != nil {
		d.placement.Cancel(rec.ID)
		fmt.Fprintf(os.Stderr, "dispatch %s: rejoining viewer below its caller: %v\n", rec.ID, err)
		return false
	}
	return d.placement.Publish(rec.ID, func() bool {
		return d.recordRejoin(s, rec.PaneID, rec.CallerSessionID, rec.CallerWindowID)
	})
}
