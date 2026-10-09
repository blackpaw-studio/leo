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
	mu       sync.Mutex
	events   []string
	showErr  error
	hideHook func()
	showHook func()
}

func (r *movingRuntime) HidePane(_ context.Context, pane, name string, _ PaneLocation) (string, error) {
	if r.hideHook != nil {
		r.hideHook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "hide "+pane+" "+name)
	return "@9", nil
}

func (r *movingRuntime) ShowPane(_ context.Context, pane, targetPane, window string, _ PaneLocation) error {
	if r.showHook != nil {
		r.showHook()
	}
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

// guardOf is the guard condition for pane in window of session, which was
// linked into linked sessions when probed: the session, the window, the
// sessions it is linked into, and the pane's membership of it, then any extra
// clauses.
func guardOf(session, window string, linked int, pane string, extra ...string) string {
	clauses := append([]string{
		"#{==:#{session_id}," + session + "}",
		"#{==:#{window_id}," + window + "}",
		"#{==:#{window_linked_sessions}," + itoa(linked) + "}",
		"#{m:*|" + pane + "|*,#{P:|#{pane_id}|}}",
	}, extra...)
	cond := clauses[len(clauses)-1]
	for i := len(clauses) - 2; i >= 0; i-- {
		cond = "#{&&:" + clauses[i] + "," + cond + "}"
	}
	return cond
}

// splitPaneAt is where the argv tests find %5: a split in window @2 of
// session $3.
var splitPaneAt = PaneLocation{SessionID: "$3", SessionName: "orch", WindowID: "@2", WindowPanes: 2, SessionWindows: 3, LinkedSessions: 1}

func TestTmuxHidePaneArgv(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{out: "@12\n"})
	window, err := r.HidePane(context.Background(), "%5", "impl·ab12", splitPaneAt)
	if err != nil || window != "@12" {
		t.Fatalf("HidePane = %q, %v", window, err)
	}
	// One command: tmux re-checks the guard and breaks the pane out together.
	want := [][]string{tmux.Args("if-shell", "-F", "-t", "$3:@2", guardOf("$3", "@2", 1, "%5"),
		"break-pane -d -P -F '#{window_id}' -s '$3:@2.%5' -t '$3:' -n 'impl·ab12'",
		"display-message -p leo-guard-failed")}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", *calls, want)
	}
}

func TestTmuxShowPaneArgv(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r)
	hidden := PaneLocation{SessionID: "$3", SessionName: "orch", WindowID: "@9", WindowPanes: 1, SessionWindows: 3, LinkedSessions: 1}
	if err := r.ShowPane(context.Background(), "%5", "%0", "@1", hidden); err != nil {
		t.Fatal(err)
	}
	// join-pane only while the window is linked nowhere else: an attach's watch
	// link would die with the emptied window.
	want := [][]string{
		tmux.Args("if-shell", "-F", "-t", "$3:@9", guardOf("$3", "@9", 1, "%5", "#{==:#{window_linked},0}"),
			"join-pane -d -v -s '$3:@9.%5' -t %0",
			"display-message -p leo-guard-failed"),
		tmux.Args("select-layout", "-t", "@1", "main-horizontal"),
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %#v\nwant %#v", *calls, want)
	}
}

func TestTmuxMovesReportAFailedGuardAndNothingElse(t *testing.T) {
	for name, move := range map[string]func(*TmuxInteractiveRuntime) error{
		"hide": func(r *TmuxInteractiveRuntime) error {
			_, err := r.HidePane(context.Background(), "%5", "x", splitPaneAt)
			return err
		},
		"show": func(r *TmuxInteractiveRuntime) error {
			return r.ShowPane(context.Background(), "%5", "%0", "@1", splitPaneAt)
		},
		"background": func(r *TmuxInteractiveRuntime) error {
			_, err := r.BackgroundPane(context.Background(), "%5", "x", splitPaneAt)
			return err
		},
		"foreground": func(r *TmuxInteractiveRuntime) error {
			bg := PaneLocation{SessionID: "$9", SessionName: dispatchViewerSession, WindowID: "@7", WindowPanes: 1, SessionWindows: 2, LinkedSessions: 1}
			_, err := r.ForegroundPane(context.Background(), "%5", "x", "$1", bg)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
			// has-session answers first for the background move; every other call
			// prints the sentinel, as tmux does when the guard is false.
			r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
				if len(args) > 2 && args[2] == "has-session" {
					return exec.Command("true")
				}
				return exec.Command("printf", "%s\n", "leo-guard-failed")
			}
			if err := move(r); !errors.Is(err, ErrPaneMoved) {
				t.Fatalf("err = %v, want ErrPaneMoved", err)
			}
		})
	}
}

func TestTmuxMovesRejectAnUnprobedPane(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r)
	if _, err := r.HidePane(context.Background(), "%5", "x", PaneLocation{}); err == nil {
		t.Fatal("hid a pane without a probed location")
	}
	unlinked := splitPaneAt
	unlinked.LinkedSessions = 0 // ids but no count: the guard would compare against nothing
	if _, err := r.HidePane(context.Background(), "%5", "x", unlinked); err == nil {
		t.Fatal("hid a pane whose linked-session count was not probed")
	}
	if _, err := r.HidePane(context.Background(), "%5; kill-server", "x", splitPaneAt); err == nil {
		t.Fatal("accepted a pane id that is not a tmux id")
	}
	if len(*calls) != 0 {
		t.Fatalf("ran tmux anyway: %#v", *calls)
	}
}

func TestTmuxMoveQuotesNamesThatWouldOtherwiseBeCommands(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	calls := scriptTmux(r, tmuxStep{out: "@12\n"})
	name := "it's; kill-server $HOME #{x}"
	if _, err := r.HidePane(context.Background(), "%5", name, splitPaneAt); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0][7] // the move command, after -L leo if-shell -F -t target cond
	if want := `break-pane -d -P -F '#{window_id}' -s '$3:@2.%5' -t '$3:' -n 'it'"'"'s; kill-server $HOME #{x}'`; got != want {
		t.Fatalf("command = %s\nwant     %s", got, want)
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
	probe := func() PaneLocation {
		loc, err := r.PaneLocation(context.Background(), pane)
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	window, err := r.HidePane(context.Background(), pane, "impl·ab12", probe())
	if err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", pane, "#{session_name} #{window_id} #{window_name}"); got != "orch "+window+" impl·ab12" {
		t.Fatalf("hidden pane at %q, want orch %s impl·ab12", got, window)
	}
	if got := tm("display-message", "-p", "-t", callerWindow, "#{window_panes}"); got != "1" {
		t.Fatalf("caller window panes = %s after hide", got)
	}
	if err := r.ShowPane(context.Background(), pane, caller, callerWindow, probe()); err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", pane, "#{window_id}"); got != callerWindow {
		t.Fatalf("rejoined pane in %s, want %s", got, callerWindow)
	}
	if _, err := r.HidePane(context.Background(), pane, "impl·ab12", probe()); err != nil {
		t.Fatal(err)
	}
	tm("kill-pane", "-t", caller)
	if err := r.ShowPane(context.Background(), pane, caller, callerWindow, probe()); err == nil {
		t.Fatal("rejoin to a gone caller pane succeeded")
	}
}

// TestTmuxMovesKeepAdversarialDispatchNamesLiteral hides and backgrounds split
// panes under names that are tmux syntax, format syntax or shell syntax. The
// name rides inside the command line of an if-shell, so it must neither split
// into a second command nor be expanded: it comes out as the window's name
// exactly as given, and the move still happens. (Only these two moves carry a
// name; ShowPane and ForegroundPane move a window or pane that already has one.)
func TestTmuxMovesKeepAdversarialDispatchNamesLiteral(t *testing.T) {
	r, tm := realTmux(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "executed")
	tm("new-session", "-d", "-s", "orch", "-x", "200", "-y", "50", "sleep 300")
	caller := tm("display-message", "-p", "-t", "=orch:", "#{pane_id}")
	probe := func(pane string) PaneLocation {
		loc, err := r.PaneLocation(ctx, pane)
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	splitPane := func() string { return tm("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", caller, "sleep 300") }

	names := []string{
		`it's`, `say "hi"`, `a ; kill-server ; b`, `#{session_name}`, `}{x}}`, `$HOME`, `a\b\;c`,
		`#(touch ` + marker + `)`,
		// What the dispatcher really passes: a sanitized, truncated label.
		viewerWindowName(Record{ID: "d-1234abcd", Name: `it's a "name"; #{x} $HOME`}),
	}
	for _, name := range names {
		want := name
		if strings.Contains(name, `\`) {
			// tmux escapes backslashes in window names whatever sets them, so ask
			// it what it keeps for this name when no command line is involved.
			scratch := splitPane()
			window := tm("break-pane", "-d", "-P", "-F", "#{window_id}", "-s", scratch, "-t", "=orch:", "-n", name)
			want = tm("display-message", "-p", "-t", window, "#{window_name}")
			tm("kill-pane", "-t", scratch)
		}
		windowName := func(pane string) string { return tm("display-message", "-p", "-t", pane, "#{window_name}") }

		hidden := splitPane()
		if _, err := r.HidePane(ctx, hidden, name, probe(hidden)); err != nil {
			t.Fatalf("hide %q: %v", name, err)
		}
		if got := probe(hidden); got.WindowPanes != 1 || windowName(hidden) != want {
			t.Fatalf("hide %q: pane in %+v named %q, want %q in a window of its own", name, got, windowName(hidden), want)
		}
		backgrounded := splitPane()
		if _, err := r.BackgroundPane(ctx, backgrounded, name, probe(backgrounded)); err != nil {
			t.Fatalf("background %q: %v", name, err)
		}
		if got := probe(backgrounded); got.SessionName != dispatchViewerSession || windowName(backgrounded) != want {
			t.Fatalf("background %q: pane in %+v named %q, want %q in %s", name, got, windowName(backgrounded), want, dispatchViewerSession)
		}
		tm("kill-pane", "-t", hidden)
		tm("kill-pane", "-t", backgrounded)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a window name was run as a command")
	}
	if got := tm("list-sessions", "-F", "#{session_name}"); !strings.Contains(got, "orch") {
		t.Fatalf("the server lost its session: %q", got)
	}
}

func TestInteractiveNeedsInputDoesNotPullPaneBack(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForHidden(t, d, id)
	// The user typed in the hidden pane and that turn hit a permission prompt.
	_ = d.Report(id, hook(t, "UserPromptSubmit", "u"))
	go d.RequestPermission(context.Background(), id, permissionPayload(t, "Bash", map[string]any{"command": "ls"}), time.Minute)
	rec := waitForStatus(t, d, id, StatusNeedsInput)
	if _, err := d.Decide(id, Decision{Behavior: "allow", RequestID: rec.NeedsInput.RequestID}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if rt.count("show") != 0 {
		t.Fatalf("events = %v, needs_input must not move the pane", rt.log())
	}
}

// layoutRuntime pauses the first caller-window relayout, the step a hide
// runs after breaking the pane out.
type layoutRuntime struct {
	*movingRuntime
	once       sync.Once
	layoutHook func()
}

func (r *layoutRuntime) ReapplyLayout(target string) error {
	r.once.Do(func() {
		if r.layoutHook != nil {
			r.layoutHook()
		}
	})
	return r.movingRuntime.ReapplyLayout(target)
}
