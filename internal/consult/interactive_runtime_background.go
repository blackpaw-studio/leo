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
	// and returns that window's id.
	BackgroundPane(ctx context.Context, pane, name string) (windowID string, err error)
	// ForegroundPane puts pane in its own window of session (a tmux session
	// target such as "$3") and returns that window's id.
	ForegroundPane(ctx context.Context, pane, name, session string) (windowID string, err error)
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
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] != pane {
			continue
		}
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
		found[0].WatchLinked = watchLinked
		return found[0], nil
	}
	return PaneLocation{}, fmt.Errorf("locate pane %s: %w", pane, ErrPaneAmbiguous)
}

// BackgroundPane moves pane into the background session, creating it when
// needed. A pane already there stays.
func (r *TmuxInteractiveRuntime) BackgroundPane(ctx context.Context, pane, name string) (string, error) {
	if err := r.ensureDispatchSession(ctx); err != nil {
		return "", err
	}
	loc, err := r.PaneLocation(ctx, pane)
	if err != nil {
		return "", err
	}
	if loc.SessionName == dispatchViewerSession {
		return loc.WindowID, nil
	}
	return r.relocatePane(ctx, pane, loc, tmux.Target(dispatchViewerSession)+":", name)
}

// ForegroundPane moves pane into session. A pane already there stays.
func (r *TmuxInteractiveRuntime) ForegroundPane(ctx context.Context, pane, name, session string) (string, error) {
	loc, err := r.PaneLocation(ctx, pane)
	if err != nil {
		return "", err
	}
	if loc.SessionID == session {
		return loc.WindowID, nil
	}
	return r.relocatePane(ctx, pane, loc, session+":", name)
}

// relocatePane moves pane to the session dest names. A pane alone in its
// window takes the window along (the window id, and any link of it into a
// watch session, survive); one sharing a window is broken out into a new
// window called name. The source is qualified by session, so a link in a
// watch session stays intact.
func (r *TmuxInteractiveRuntime) relocatePane(ctx context.Context, pane string, loc PaneLocation, dest, name string) (string, error) {
	if loc.WindowPanes == 1 {
		if loc.SessionWindows == 1 {
			return "", fmt.Errorf("move pane %s: its window is the only one of session %s, and moving it would end the session", pane, loc.SessionName)
		}
		if err := r.run(ctx, "move-window", "-d", "-s", loc.SessionID+":"+loc.WindowID, "-t", dest); err != nil {
			return "", fmt.Errorf("move window %s of pane %s: %w", loc.WindowID, pane, err)
		}
		return loc.WindowID, nil
	}
	if err := r.run(ctx, "break-pane", "-d", "-s", pane, "-t", dest, "-n", name); err != nil {
		return "", fmt.Errorf("break pane %s out: %w", pane, err)
	}
	moved, err := r.PaneLocation(ctx, pane)
	if err != nil {
		return "", err
	}
	return moved.WindowID, nil
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
