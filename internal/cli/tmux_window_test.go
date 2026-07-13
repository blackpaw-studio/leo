package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// windowExecStub records every agentExecCommand invocation and lets a test
// script canned stdout for specific tmux subcommands (list-windows is the
// only one ensureTmuxWindow reads output from; the rest are fire-and-forget
// mutations that just need to succeed).
type windowExecStub struct {
	calls          [][]string
	listWindows    string // stdout returned for a "list-windows" call
	listWindowsErr bool
}

func (s *windowExecStub) fn(name string, args ...string) *exec.Cmd {
	call := append([]string{name}, args...)
	s.calls = append(s.calls, call)
	for _, a := range args {
		if a == "list-windows" {
			if s.listWindowsErr {
				return exec.Command("false")
			}
			return exec.Command("printf", "%s", s.listWindows)
		}
	}
	return exec.Command("true")
}

func withWindowExecStub(t *testing.T) *windowExecStub {
	t.Helper()
	stub := &windowExecStub{}
	old := agentExecCommand
	agentExecCommand = stub.fn
	t.Cleanup(func() { agentExecCommand = old })
	return stub
}

// callNamed finds the first recorded call whose args contain subcommand,
// or nil.
func callNamed(calls [][]string, subcommand string) []string {
	for _, c := range calls {
		for _, a := range c {
			if a == subcommand {
				return c
			}
		}
	}
	return nil
}

func TestEnsureTmuxWindowCreatesWhenAbsent(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindows = "" // no windows at all

	err := ensureTmuxWindow("/usr/bin/tmux", "leo-foo", "tui", []string{"opencode", "attach", "http://x"}, "ses_1")
	if err != nil {
		t.Fatalf("ensureTmuxWindow: %v", err)
	}

	if callNamed(stub.calls, "kill-window") != nil {
		t.Errorf("expected no kill-window call when the window is absent, got %v", stub.calls)
	}
	newWindow := callNamed(stub.calls, "new-window")
	if newWindow == nil {
		t.Fatalf("expected a new-window call, got %v", stub.calls)
	}
	wantCmd := "'opencode' 'attach' 'http://x'"
	if newWindow[len(newWindow)-1] != wantCmd {
		t.Errorf("new-window command = %q, want %q", newWindow[len(newWindow)-1], wantCmd)
	}
	setOpt := callNamed(stub.calls, "set-option")
	if setOpt == nil {
		t.Fatalf("expected a set-option call tagging the window key, got %v", stub.calls)
	}
	if setOpt[len(setOpt)-1] != "ses_1" || setOpt[len(setOpt)-2] != leoTUIWindowKeyOption {
		t.Errorf("set-option tail = %v, want [...%q ses_1]", setOpt, leoTUIWindowKeyOption)
	}
}

func TestEnsureTmuxWindowReusesWhenKeyMatches(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindows = "tui\tses_1\n"

	err := ensureTmuxWindow("/usr/bin/tmux", "leo-foo", "tui", []string{"opencode", "attach", "http://x"}, "ses_1")
	if err != nil {
		t.Fatalf("ensureTmuxWindow: %v", err)
	}

	if callNamed(stub.calls, "kill-window") != nil {
		t.Errorf("expected no kill-window when the key matches, got %v", stub.calls)
	}
	if callNamed(stub.calls, "new-window") != nil {
		t.Errorf("expected no new-window when the key matches, got %v", stub.calls)
	}
	if callNamed(stub.calls, "set-option") != nil {
		t.Errorf("expected no set-option when reusing an existing window, got %v", stub.calls)
	}
	// Only the list-windows probe itself should have run.
	if len(stub.calls) != 1 {
		t.Errorf("expected exactly 1 call (the list-windows probe), got %d: %v", len(stub.calls), stub.calls)
	}
}

func TestEnsureTmuxWindowRecreatesWhenKeyStale(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindows = "tui\tses_old\n"

	err := ensureTmuxWindow("/usr/bin/tmux", "leo-foo", "tui", []string{"opencode", "attach", "http://x"}, "ses_new")
	if err != nil {
		t.Fatalf("ensureTmuxWindow: %v", err)
	}

	killIdx, newIdx, setIdx := -1, -1, -1
	for i, c := range stub.calls {
		for _, a := range c {
			switch a {
			case "kill-window":
				killIdx = i
			case "new-window":
				newIdx = i
			case "set-option":
				setIdx = i
			}
		}
	}
	if killIdx == -1 {
		t.Fatalf("expected a kill-window call for a stale key, got %v", stub.calls)
	}
	if newIdx == -1 || newIdx < killIdx {
		t.Fatalf("expected new-window after kill-window, got %v", stub.calls)
	}
	if setIdx == -1 || setIdx < newIdx {
		t.Fatalf("expected set-option after new-window, got %v", stub.calls)
	}
}

func TestEnsureTmuxWindowQuotesEveryCmdToken(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindows = ""

	cmd := []string{"opencode", "attach", "http://127.0.0.1:1", "-p", "pw with space"}
	if err := ensureTmuxWindow("/usr/bin/tmux", "leo-foo", "tui", cmd, ""); err != nil {
		t.Fatalf("ensureTmuxWindow: %v", err)
	}

	newWindow := callNamed(stub.calls, "new-window")
	if newWindow == nil {
		t.Fatalf("expected a new-window call, got %v", stub.calls)
	}
	got := newWindow[len(newWindow)-1]
	want := "'opencode' 'attach' 'http://127.0.0.1:1' '-p' 'pw with space'"
	if got != want {
		t.Errorf("new-window command = %q, want %q", got, want)
	}
	if strings.Count(got, "'") == 0 {
		t.Errorf("expected quoted tokens, got %q", got)
	}
}

func TestEnsureTmuxWindowUnreadableSessionTreatedAsAbsent(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindowsErr = true // e.g. the tmux session doesn't exist yet

	err := ensureTmuxWindow("/usr/bin/tmux", "leo-foo", "tui", []string{"opencode", "attach", "http://x"}, "ses_1")
	if err != nil {
		t.Fatalf("ensureTmuxWindow: %v", err)
	}
	if callNamed(stub.calls, "new-window") == nil {
		t.Fatalf("expected a new-window call when list-windows fails, got %v", stub.calls)
	}
}

// TestAttachViaDriverTmuxFlavorEnsuresSelectsAndAttaches is the end-to-end
// check for attachViaDriver's tmux-flavored branch: ensure the window (no
// window exists yet), select it, then fall through to attachTmuxSession's
// normal outside-tmux exec path (agentSyscallExec, replacing this process).
func TestAttachViaDriverTmuxFlavorEnsuresSelectsAndAttaches(t *testing.T) {
	stub := withWindowExecStub(t)
	stub.listWindows = ""
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)

	var execedArgv []string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	spec := harness.AttachSpec{
		TmuxSession: "leo-foo",
		WindowName:  "tui",
		WindowCmd:   []string{"opencode", "attach", "http://x"},
		WindowKey:   "ses_1",
	}
	if err := attachViaDriver(config.HostResolution{Localhost: true}, spec, attachOptions{}); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}

	if callNamed(stub.calls, "new-window") == nil {
		t.Fatalf("expected the window to be created, got %v", stub.calls)
	}
	selectCall := callNamed(stub.calls, "select-window")
	if selectCall == nil {
		t.Fatalf("expected a select-window call, got %v", stub.calls)
	}
	wantTarget := "=leo-foo:tui"
	if selectCall[len(selectCall)-1] != wantTarget {
		t.Errorf("select-window target = %q, want %q", selectCall[len(selectCall)-1], wantTarget)
	}
	// attachTmuxSession's outside-tmux branch execs tmux attach against the
	// TmuxSession (not the window) — selecting the window beforehand is what
	// makes the attach land there.
	if len(execedArgv) == 0 || execedArgv[len(execedArgv)-1] != "=leo-foo" {
		t.Fatalf("expected the final tmux attach target to be the session, got argv=%v", execedArgv)
	}
}
