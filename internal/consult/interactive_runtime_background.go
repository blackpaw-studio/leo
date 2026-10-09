package consult

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// DispatchWatchSessionPrefix names the throwaway tmux sessions `leo dispatch
// attach` links a dispatch window into. A link there is a view of the pane,
// not a place it lives.
const DispatchWatchSessionPrefix = "_watch-"

// PaneLocation is where a live dispatch pane sits now.
type PaneLocation struct {
	SessionID, SessionName, WindowID string
	// WindowPanes is how many panes share the pane's window; SessionWindows
	// how many windows its session has.
	WindowPanes, SessionWindows int
	// LinkedSessions is how many sessions the pane's window is linked into,
	// watch sessions included (#{window_linked_sessions}): 1 for a window in its
	// session alone. A guarded move holds it to the count seen at the probe, so
	// a link made after the probe, by an attach or into another real session,
	// refuses the move.
	LinkedSessions int
	// WatchLinked is set while `leo dispatch attach` has the pane's window
	// linked into a watch session of its own. Emptying that window (join-pane)
	// would end the attach; moving it whole keeps the link.
	WatchLinked bool
}

var (
	// ErrPaneNotFound means no tmux session holds the pane.
	ErrPaneNotFound = errors.New("pane not found")
	// ErrPaneAmbiguous means the pane's window is linked into more than one
	// real session, so nobody can say where it lives.
	ErrPaneAmbiguous = errors.New("pane is linked into more than one session")
)

// backgroundMoverRuntime moves a live dispatch pane between its caller's tmux
// session and the background one (leo-dispatch). The pane id (%N) survives
// every move, so the rest of the runtime keeps addressing the pane by it.
type backgroundMoverRuntime interface {
	PaneLocation(ctx context.Context, pane string) (PaneLocation, error)
	// BackgroundPane puts pane in its own window of the background session
	// and returns that window's id. from is where the caller probed the pane;
	// the move happens only while it is still there (see guardedMove), and
	// ErrPaneMoved says it was not.
	BackgroundPane(ctx context.Context, pane, name string, from PaneLocation) (windowID string, err error)
	// ForegroundPane puts pane in its own window of session (a tmux session
	// target such as "$3") and returns that window's id. from is as for
	// BackgroundPane.
	ForegroundPane(ctx context.Context, pane, name, session string, from PaneLocation) (windowID string, err error)
}

const paneLocationFormat = "#{pane_id}\t#{session_id}\t#{session_name}\t#{window_id}\t#{window_panes}\t#{session_windows}"

// PaneLocation reports the session and window holding pane. Windows
// `leo dispatch attach` linked into its own watch sessions are not counted: a
// pane linked into two real sessions is ErrPaneAmbiguous.
func (r *TmuxInteractiveRuntime) PaneLocation(ctx context.Context, pane string) (PaneLocation, error) {
	out, err := r.output(ctx, "list-panes", "-a", "-F", paneLocationFormat)
	if err != nil {
		return PaneLocation{}, fmt.Errorf("locate pane %s: %w", pane, err)
	}
	var found []PaneLocation
	watchLinked := false
	linkedSessions := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] != pane {
			continue
		}
		linkedSessions++
		if strings.HasPrefix(f[2], DispatchWatchSessionPrefix) {
			watchLinked = true
			continue
		}
		panes, panesErr := strconv.Atoi(f[4])
		windows, windowsErr := strconv.Atoi(f[5])
		if panesErr != nil || windowsErr != nil {
			continue
		}
		found = append(found, PaneLocation{SessionID: f[1], SessionName: f[2], WindowID: f[3], WindowPanes: panes, SessionWindows: windows})
	}
	switch len(found) {
	case 0:
		return PaneLocation{}, fmt.Errorf("locate pane %s: %w", pane, ErrPaneNotFound)
	case 1:
		found[0].WatchLinked, found[0].LinkedSessions = watchLinked, linkedSessions
		return found[0], nil
	}
	return PaneLocation{}, fmt.Errorf("locate pane %s: %w", pane, ErrPaneAmbiguous)
}

// BackgroundPane moves pane into the background session, creating it when
// needed. A pane already there stays.
func (r *TmuxInteractiveRuntime) BackgroundPane(ctx context.Context, pane, name string, from PaneLocation) (string, error) {
	if from.SessionName == dispatchViewerSession {
		return from.WindowID, nil
	}
	if err := r.ensureDispatchSession(ctx); err != nil {
		return "", err
	}
	return r.relocatePane(ctx, pane, from, tmux.Target(dispatchViewerSession)+":", name)
}

// ForegroundPane moves pane into session. A pane already there stays.
func (r *TmuxInteractiveRuntime) ForegroundPane(ctx context.Context, pane, name, session string, from PaneLocation) (string, error) {
	if from.SessionID == session {
		return from.WindowID, nil
	}
	return r.relocatePane(ctx, pane, from, session+":", name)
}

// relocatePane moves pane, as probed at from, to the session dest names. A pane
// alone in its window takes the window along (the window id, and any link of it
// into a watch session, survive); one sharing a window is broken out into a new
// window called name. The source is qualified by session, so a link in a watch
// session stays intact. Besides the pane's place, the guard holds the pane
// count the choice of move rested on, and the session's window count, since
// moving its last window would end the session.
func (r *TmuxInteractiveRuntime) relocatePane(ctx context.Context, pane string, from PaneLocation, dest, name string) (string, error) {
	if from.WindowPanes == 1 {
		if from.SessionWindows == 1 {
			return "", fmt.Errorf("move pane %s: its window is the only one of session %s, and moving it would end the session", pane, from.SessionName)
		}
		if _, err := r.guardedMove(ctx, pane, from, []string{windowPanesAre(1), sessionWindowsAre(from.SessionWindows)},
			"move-window", "-d", "-s", from.SessionID+":"+from.WindowID, "-t", dest); err != nil {
			return "", fmt.Errorf("move window %s of pane %s: %w", from.WindowID, pane, err)
		}
		return from.WindowID, nil
	}
	window, err := r.guardedMove(ctx, pane, from, []string{windowPanesAre(from.WindowPanes)},
		"break-pane", "-d", "-P", "-F", "#{window_id}", "-s", paneRef(pane, from), "-t", dest, "-n", name)
	if err != nil {
		return "", fmt.Errorf("break pane %s out: %w", pane, err)
	}
	return window, nil
}

// ensureDispatchSession makes sure the background session exists. Another
// simultaneous dispatch may create it between the probe and new-session, so
// only give up when it is still absent.
func (r *TmuxInteractiveRuntime) ensureDispatchSession(ctx context.Context) error {
	target := tmux.Target(dispatchViewerSession)
	if r.run(ctx, "has-session", "-t", target) == nil {
		return nil
	}
	if r.run(ctx, "new-session", "-d", "-s", dispatchViewerSession) != nil && r.run(ctx, "has-session", "-t", target) != nil {
		return fmt.Errorf("create fallback tmux session %q", dispatchViewerSession)
	}
	return nil
}
