package consult

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// tmuxStep is one scripted tmux answer: stdout, or a failing exit.
type tmuxStep struct {
	out  string
	fail bool
}

// scriptTmux points r at a scripted tmux and returns the argv it was called
// with. Calls beyond the script succeed with no output.
func scriptTmux(r *TmuxInteractiveRuntime, steps ...tmuxStep) *[][]string {
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) > len(steps) {
			return exec.Command("true")
		}
		step := steps[len(calls)-1]
		if step.fail {
			return exec.Command("false")
		}
		return exec.Command("printf", "%s", step.out)
	}
	return &calls
}

func panesRow(pane, sessionID, session, window string, windowPanes, sessionWindows int) string {
	return strings.Join([]string{pane, sessionID, session, window, itoa(windowPanes), itoa(sessionWindows)}, "\t") + "\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

var listPanesAll = tmux.Args("list-panes", "-a", "-F", "#{pane_id}\t#{session_id}\t#{session_name}\t#{window_id}\t#{window_panes}\t#{session_windows}")

func TestPaneLocationReadsTheLiveWindow(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{out: panesRow("%4", "$1", "orch", "@2", 3, 2) + panesRow("%5", "$1", "orch", "@7", 1, 2) + panesRow("%6", "$1", "orch", "@8", 1, 2)})
	loc, err := r.PaneLocation(context.Background(), "%5")
	if err != nil {
		t.Fatal(err)
	}
	want := PaneLocation{SessionID: "$1", SessionName: "orch", WindowID: "@7", WindowPanes: 1, SessionWindows: 2}
	if loc != want {
		t.Fatalf("location = %+v, want %+v", loc, want)
	}
	if !reflect.DeepEqual(*calls, [][]string{listPanesAll}) {
		t.Fatalf("argv = %#v", *calls)
	}
}

func TestPaneLocationIgnoresWatchSessionLinks(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	scriptTmux(r, tmuxStep{out: panesRow("%5", "$9", DispatchWatchSessionPrefix+"d-1-ab", "@7", 1, 1) + panesRow("%5", "$1", "orch", "@7", 1, 2)})
	loc, err := r.PaneLocation(context.Background(), "%5")
	if err != nil || loc.SessionName != "orch" {
		t.Fatalf("location = %+v, %v; want the real session, not the attach link", loc, err)
	}
}

func TestPaneLocationErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		step tmuxStep
		want error
	}{
		"pane absent":            {tmuxStep{out: panesRow("%9", "$1", "orch", "@2", 1, 1)}, ErrPaneNotFound},
		"linked into two places": {tmuxStep{out: panesRow("%5", "$1", "orch", "@7", 1, 2) + panesRow("%5", "$2", "other", "@7", 1, 2)}, ErrPaneAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
			scriptTmux(r, tc.step)
			if _, err := r.PaneLocation(context.Background(), "%5"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	scriptTmux(r, tmuxStep{fail: true})
	if _, err := r.PaneLocation(context.Background(), "%5"); err == nil || errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("err = %v, want a tmux failure that is not 'pane not found'", err)
	}
}

var hasBackground = tmux.Args("has-session", "-t", "=leo-dispatch")

func TestBackgroundPaneMovesALoneWindowWithItsWindowId(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{}, tmuxStep{out: panesRow("%5", "$1", "orch", "@7", 1, 3)})
	window, err := r.BackgroundPane(context.Background(), "%5", "impl·ab12")
	if err != nil || window != "@7" {
		t.Fatalf("BackgroundPane = %q, %v", window, err)
	}
	want := [][]string{
		hasBackground,
		listPanesAll,
		tmux.Args("move-window", "-d", "-s", "$1:@7", "-t", "=leo-dispatch:"),
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", *calls, want)
	}
}

func TestBackgroundPaneBreaksASplitOutAndReprobesItsWindow(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{}, tmuxStep{out: panesRow("%4", "$1", "orch", "@2", 2, 3) + panesRow("%5", "$1", "orch", "@2", 2, 3)}, tmuxStep{}, tmuxStep{out: panesRow("%5", "$9", "leo-dispatch", "@12", 1, 2)})
	window, err := r.BackgroundPane(context.Background(), "%5", "impl·ab12")
	if err != nil || window != "@12" {
		t.Fatalf("BackgroundPane = %q, %v; want the re-probed window id", window, err)
	}
	want := [][]string{
		hasBackground,
		listPanesAll,
		tmux.Args("break-pane", "-d", "-s", "%5", "-t", "=leo-dispatch:", "-n", "impl·ab12"),
		listPanesAll,
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", *calls, want)
	}
}

func TestBackgroundPaneCreatesTheSessionOnDemand(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{fail: true}, tmuxStep{}, tmuxStep{out: panesRow("%5", "$1", "orch", "@7", 1, 3)})
	if _, err := r.BackgroundPane(context.Background(), "%5", "impl·ab12"); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[1]; !reflect.DeepEqual(got, tmux.Args("new-session", "-d", "-s", "leo-dispatch")) {
		t.Fatalf("second call = %#v, want new-session", got)
	}
}

func TestBackgroundPaneIsIdempotentInTheBackgroundSession(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{}, tmuxStep{out: panesRow("%5", "$9", "leo-dispatch", "@7", 1, 2)})
	window, err := r.BackgroundPane(context.Background(), "%5", "impl·ab12")
	if err != nil || window != "@7" || len(*calls) != 2 {
		t.Fatalf("BackgroundPane = %q, %v after %d calls; want no move", window, err, len(*calls))
	}
}

func TestBackgroundPaneNeverEmptiesASession(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{}, tmuxStep{out: panesRow("%5", "$1", "orch", "@7", 1, 1)})
	if _, err := r.BackgroundPane(context.Background(), "%5", "impl·ab12"); err == nil {
		t.Fatal("moving the only window of a session destroys the session")
	}
	for _, call := range *calls {
		if len(call) > 2 && call[2] == "move-window" {
			t.Fatalf("moved anyway: %#v", *calls)
		}
	}
}

func TestForegroundPaneMovesALoneWindowBackToTheCallerSession(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{out: panesRow("%5", "$9", "leo-dispatch", "@7", 1, 2)})
	window, err := r.ForegroundPane(context.Background(), "%5", "impl·ab12", "$1")
	if err != nil || window != "@7" {
		t.Fatalf("ForegroundPane = %q, %v", window, err)
	}
	want := [][]string{listPanesAll, tmux.Args("move-window", "-d", "-s", "$9:@7", "-t", "$1:")}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", *calls, want)
	}
}

func TestForegroundPaneLeavesAPaneAlreadyInTheCallerSession(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{out: panesRow("%5", "$1", "orch", "@7", 1, 3)})
	window, err := r.ForegroundPane(context.Background(), "%5", "impl·ab12", "$1")
	if err != nil || window != "@7" || len(*calls) != 1 {
		t.Fatalf("ForegroundPane = %q, %v after %d calls; want no move", window, err, len(*calls))
	}
}

// realTmux starts a throwaway tmux server on a private socket and points a
// runtime at it (the runtime's -L leo socket is rewritten to the private -S
// one). tm runs a tmux command against the same server and returns its output.
func realTmux(t *testing.T) (*TmuxInteractiveRuntime, func(args ...string) string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	dir, err := os.MkdirTemp("/tmp", "lt")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, "-S", sock, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(tmuxPath, append([]string{"-S", sock}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	r := NewInteractiveRuntime("x", nil, nil, tmuxPath, "/opt/leo")
	r.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "-L" {
			args = append([]string{"-S", sock}, args[2:]...)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	return r, tm
}

// TestTmuxBackgroundRoundTripKeepsPaneIdsThroughRealTmux moves a split pane
// and a lone-window pane to the background session and back, with a decoy
// session created last (the one an untargeted command would pick) and a watch
// session linked to the lone window.
func TestTmuxBackgroundRoundTripKeepsPaneIdsThroughRealTmux(t *testing.T) {
	r, tm := realTmux(t)
	ctx := context.Background()
	tm("new-session", "-d", "-s", "orch", "-x", "200", "-y", "50", "sleep 300")
	orch := tm("display-message", "-p", "-t", "=orch:", "#{session_id}")
	caller := tm("display-message", "-p", "-t", "=orch:", "#{pane_id}")
	callerWindow := tm("display-message", "-p", "-t", "=orch:", "#{window_id}")
	split := tm("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", caller, "sleep 300")
	lone := tm("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=orch:", "-n", "lone·cd34", "sleep 300")
	loneWindow := tm("display-message", "-p", "-t", lone, "#{window_id}")
	tm("new-session", "-d", "-s", DispatchWatchSessionPrefix+"d-1-x", "sleep 300")
	tm("link-window", "-d", "-s", loneWindow, "-t", "="+DispatchWatchSessionPrefix+"d-1-x:")
	tm("new-session", "-d", "-s", "decoy", "sleep 300")
	where := func(pane string) string {
		return tm("display-message", "-p", "-t", pane, "#{pane_id} #{window_panes}")
	}
	sessionOf := func(pane string) string {
		loc, err := r.PaneLocation(ctx, pane)
		if err != nil {
			t.Fatalf("PaneLocation(%s): %v", pane, err)
		}
		return loc.SessionName
	}

	// A split pane goes to the background session as a window of its own.
	window, err := r.BackgroundPane(ctx, split, "impl·ab12")
	if err != nil {
		t.Fatal(err)
	}
	if sessionOf(split) != "leo-dispatch" || where(split) != split+" 1" || window == callerWindow {
		t.Fatalf("split pane at %s/%s in %s", sessionOf(split), where(split), window)
	}
	if got := tm("display-message", "-p", "-t", callerWindow, "#{window_panes}"); got != "1" {
		t.Fatalf("caller window panes = %s after the pane left", got)
	}
	if got := tm("display-message", "-p", "-t", window, "#{window_name}"); got != "impl·ab12" {
		t.Fatalf("background window named %q", got)
	}

	// It comes back as a window of the caller session, then as a split.
	back, err := r.ForegroundPane(ctx, split, "impl·ab12", orch)
	if err != nil || back != window {
		t.Fatalf("ForegroundPane = %q, %v; want the same window %s", back, err, window)
	}
	if sessionOf(split) != "orch" {
		t.Fatalf("split pane in %s, want orch", sessionOf(split))
	}
	if err := r.ShowPane(ctx, split, caller, callerWindow); err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", split, "#{window_id}"); got != callerWindow {
		t.Fatalf("rejoined pane in %s, want %s", got, callerWindow)
	}

	// A lone window moves whole: same window id, and the watch link survives.
	moved, err := r.BackgroundPane(ctx, lone, "lone·cd34")
	if err != nil || moved != loneWindow {
		t.Fatalf("BackgroundPane(lone) = %q, %v; want %s", moved, err, loneWindow)
	}
	if sessionOf(lone) != "leo-dispatch" {
		t.Fatalf("lone pane in %s", sessionOf(lone))
	}
	if links := tm("list-windows", "-t", "="+DispatchWatchSessionPrefix+"d-1-x:", "-F", "#{window_id}"); !strings.Contains(links, loneWindow) {
		t.Fatalf("watch session lost its link: %q", links)
	}
	if _, err := r.ForegroundPane(ctx, lone, "lone·cd34", orch); err != nil || sessionOf(lone) != "orch" {
		t.Fatalf("ForegroundPane(lone): %v, in %s", err, sessionOf(lone))
	}
	if where(lone) != lone+" 1" || where(split) != split+" 2" {
		t.Fatalf("pane ids drifted: lone %s split %s", where(lone), where(split))
	}
}

func TestTmuxBackgroundPaneRefusesToEndAnAlmostEmptySession(t *testing.T) {
	r, tm := realTmux(t)
	tm("new-session", "-d", "-s", "solo", "sleep 300")
	pane := tm("display-message", "-p", "-t", "=solo:", "#{pane_id}")
	if _, err := r.BackgroundPane(context.Background(), pane, "x"); err == nil {
		t.Fatal("moved the only window of its session")
	}
	if got := tm("list-sessions", "-F", "#{session_name}"); !strings.Contains(got, "solo") {
		t.Fatalf("session ended: %q", got)
	}
}
