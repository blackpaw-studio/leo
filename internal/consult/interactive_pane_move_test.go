package consult

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// movingRuntime records pane moves and which pane each injection targeted,
// in order, so tests can assert a hidden pane is rejoined before a send.
type movingRuntime struct {
	*fakeInteractiveRuntime
	mu      sync.Mutex
	events  []string
	showErr error
}

func (r *movingRuntime) HidePane(_ context.Context, pane, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "hide "+pane+" "+name)
	return "@9", nil
}

func (r *movingRuntime) ShowPane(_ context.Context, pane, targetPane, window string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "show "+pane+" "+targetPane+" "+window)
	return r.showErr
}

func (r *movingRuntime) Inject(ctx context.Context, pane, text string, arm func() error) error {
	r.mu.Lock()
	r.events = append(r.events, "inject "+pane)
	r.mu.Unlock()
	return r.fakeInteractiveRuntime.Inject(ctx, pane, text, arm)
}

func (r *movingRuntime) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *movingRuntime) count(prefix string) int {
	n := 0
	for _, e := range r.log() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func waitForEvents(t *testing.T, r *movingRuntime, prefix string, n int) {
	t.Helper()
	deadline := time.After(time.Second)
	for r.count(prefix) < n {
		select {
		case <-deadline:
			t.Fatalf("want %d %q events, got %v", n, prefix, r.log())
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// startSplitCodex starts a codex interactive dispatch placed as a split in
// the caller's window and drives its opening turn to idle.
func startSplitCodex(t *testing.T) (*Dispatcher, *movingRuntime, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	rt := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{
		Template: "codex", Name: "impl", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive,
		CallerPaneID: "%0", CallerSessionID: "leo-orch", CallerWindowID: "@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Placement != "split" {
		t.Fatalf("placement = %q, want split", started.Placement)
	}
	waitForInjection(t, rt.fakeInteractiveRuntime)
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	return d, rt, started.ID
}

func TestInteractiveIdleHidesSplitPane(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForEvents(t, rt, "hide", 1)
	rec, _ := d.Get(id)
	if got := rt.log()[len(rt.log())-1]; got != "hide %1 "+viewerWindowName(rec) {
		t.Fatalf("hide event = %q", got)
	}
	deadline := time.Now().Add(time.Second)
	for rec.ViewerKind != "hidden" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		rec, _ = d.Get(id)
	}
	if rec.ViewerKind != "hidden" || rec.ViewerWindowID != "@9" || rec.PaneID != "%1" {
		t.Fatalf("record kind=%q window=%q pane=%q, want hidden in @9 keeping pane %%1", rec.ViewerKind, rec.ViewerWindowID, rec.PaneID)
	}
	rt.fakeInteractiveRuntime.mu.Lock()
	layouts := append([]string(nil), rt.layouts...)
	rt.fakeInteractiveRuntime.mu.Unlock()
	if !reflect.DeepEqual(layouts, []string{"@1"}) {
		t.Fatalf("layouts = %v, want caller window re-tiled once", layouts)
	}
}

func TestInteractiveSendRejoinsHiddenPaneBeforeInjectSamePaneID(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForHidden(t, d, id)
	if _, err := d.Send(context.Background(), id, "next"); err != nil {
		t.Fatal(err)
	}
	events := rt.log()
	tail := events[len(events)-2:]
	if !reflect.DeepEqual(tail, []string{"show %1 %0 @1", "inject %1"}) {
		t.Fatalf("events = %v, want rejoin then inject into the same pane id", events)
	}
	rec, _ := d.Get(id)
	if rec.ViewerKind != "split" || rec.PaneID != "%1" {
		t.Fatalf("record kind=%q pane=%q after rejoin", rec.ViewerKind, rec.PaneID)
	}
	// The follow-up finishing hides it again.
	_ = d.Report(id, hook(t, "UserPromptSubmit", "b"))
	_ = d.Report(id, hook(t, "Stop", "b"))
	waitForEvents(t, rt, "hide", 2)
}

func TestInteractiveRejoinFailureLeavesPaneInItsWindow(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForHidden(t, d, id)
	rt.showErr = errors.New("can't find pane: %0")
	if _, err := d.Send(context.Background(), id, "next"); err != nil {
		t.Fatal(err)
	}
	if rt.count("inject %1") != 2 {
		t.Fatalf("events = %v, want the follow-up injected into %%1 anyway", rt.log())
	}
	rec, _ := d.Get(id)
	if rec.ViewerKind != "window" || rec.ViewerWindowID != "@9" || rec.PaneID != "%1" {
		t.Fatalf("record kind=%q window=%q pane=%q, want left in its own window", rec.ViewerKind, rec.ViewerWindowID, rec.PaneID)
	}
	_ = d.Report(id, hook(t, "UserPromptSubmit", "b"))
	_ = d.Report(id, hook(t, "Stop", "b"))
	time.Sleep(20 * time.Millisecond)
	if rt.count("hide") != 1 {
		t.Fatalf("events = %v, a pane in its own window must not be broken out again", rt.log())
	}
}

func TestInteractiveUserTurnDoesNotMovePane(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForHidden(t, d, id)
	_ = d.Report(id, hook(t, "UserPromptSubmit", "u"))
	_ = d.Report(id, hook(t, "Stop", "u"))
	time.Sleep(20 * time.Millisecond)
	if rt.count("show") != 0 || rt.count("hide") != 1 {
		t.Fatalf("events = %v, a user-typed turn must not move the pane", rt.log())
	}
}

func TestInteractiveWindowPlacementNeverMoves(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt.fakeInteractiveRuntime)
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	time.Sleep(20 * time.Millisecond)
	if rt.count("hide") != 0 {
		t.Fatalf("events = %v, a window-placed pane has nothing to hide", rt.log())
	}
}

func waitForHidden(t *testing.T, d *Dispatcher, id string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		rec, _ := d.Get(id)
		if rec.ViewerKind == "hidden" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("pane never hidden; kind=%q", rec.ViewerKind)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestTmuxHidePaneArgv(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return exec.Command("printf", "$3\n")
		}
		return exec.Command("printf", "@12\n")
	}
	window, err := r.HidePane(context.Background(), "%5", "impl·ab12")
	if err != nil || window != "@12" {
		t.Fatalf("HidePane = %q, %v", window, err)
	}
	want := [][]string{
		tmux.Args("display-message", "-p", "-t", "%5", "#{session_id}"),
		tmux.Args("break-pane", "-d", "-P", "-F", "#{window_id}", "-s", "%5", "-t", "$3:", "-n", "impl·ab12"),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", calls, want)
	}
}

func TestTmuxShowPaneArgv(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		return exec.Command("true")
	}
	if err := r.ShowPane(context.Background(), "%5", "%0", "@1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		tmux.Args("join-pane", "-d", "-v", "-s", "%5", "-t", "%0"),
		tmux.Args("select-layout", "-t", "@1", "main-horizontal"),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", calls, want)
	}
}

// TestTmuxPaneMoveRoundTripsThroughRealTmux drives HidePane and ShowPane
// against a throwaway tmux server (the runtime's -L leo socket is rewritten
// to a private -S socket) with a decoy session created last, the one tmux
// would pick for an untargeted break-pane.
func TestTmuxPaneMoveRoundTripsThroughRealTmux(t *testing.T) {
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
	tm("new-session", "-d", "-s", "orch", "-x", "200", "-y", "50", "sleep 300")
	caller := tm("display-message", "-p", "-t", "orch:", "#{pane_id}")
	callerWindow := tm("display-message", "-p", "-t", "orch:", "#{window_id}")
	pane := tm("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", caller, "sleep 300")
	tm("new-session", "-d", "-s", "decoy", "sleep 300")

	r := NewInteractiveRuntime("x", nil, nil, tmuxPath, "/opt/leo")
	r.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "-L" {
			args = append([]string{"-S", sock}, args[2:]...)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	window, err := r.HidePane(context.Background(), pane, "impl·ab12")
	if err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", pane, "#{session_name} #{window_id} #{window_name}"); got != "orch "+window+" impl·ab12" {
		t.Fatalf("hidden pane at %q, want orch %s impl·ab12", got, window)
	}
	if got := tm("display-message", "-p", "-t", callerWindow, "#{window_panes}"); got != "1" {
		t.Fatalf("caller window panes = %s after hide", got)
	}
	if err := r.ShowPane(context.Background(), pane, caller, callerWindow); err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", pane, "#{window_id}"); got != callerWindow {
		t.Fatalf("rejoined pane in %s, want %s", got, callerWindow)
	}
	if _, err := r.HidePane(context.Background(), pane, "impl·ab12"); err != nil {
		t.Fatal(err)
	}
	tm("kill-pane", "-t", caller)
	if err := r.ShowPane(context.Background(), pane, caller, callerWindow); err == nil {
		t.Fatal("rejoin to a gone caller pane succeeded")
	}
}
