package consult

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// isolatedTmux starts a tmux server in its own TMUX_TMPDIR (never the live
// leo socket) with one session, and returns the command seam that drives
// it plus the session's pane id.
func isolatedTmux(t *testing.T, session string) (func(name string, args ...string) *exec.Cmd, string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	dir, err := os.MkdirTemp("/tmp", "leo-roster-")
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "TMUX_TMPDIR="+dir, "TMUX=")
	command := func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, args...)
		cmd.Env = env
		return cmd
	}
	t.Cleanup(func() {
		_ = command("tmux", "-L", "leo", "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	out, err := command("tmux", "-L", "leo", "new-session", "-d", "-s", session, "-P", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("starting isolated tmux: %v", err)
	}
	return command, strings.TrimSpace(string(out))
}

func showSessionOption(t *testing.T, command func(string, ...string) *exec.Cmd, session, option string) string {
	t.Helper()
	out, _ := command("tmux", "-L", "leo", "show-options", "-v", "-t", session+":", option).Output()
	return strings.TrimSpace(string(out))
}

func TestViewerRosterHidesABridgedCallersDispatchesAndRestoresThemOnDisconnect(t *testing.T) {
	run, pane := isolatedTmux(t, "leo-worker")
	var calls [][]string
	isBridged := false
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return run(name, args...)
		},
		BridgeConnected: func(key string) bool { return isBridged && key == "worker" },
	}
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Name: "build", Status: StatusRunning, StartedAt: now, ViewerPaneID: pane, CallerBridgeKey: "worker"}

	v.UpdateRoster([]Record{rec}, now)
	if got := showSessionOption(t, run, "leo-worker", "@leo_roster"); !strings.Contains(got, "build") {
		t.Fatalf("unbridged roster = %q, want the dispatch", got)
	}
	if got := showSessionOption(t, run, "leo-worker", "status"); got != "2" {
		t.Fatalf("unbridged status = %q, want 2", got)
	}

	isBridged = true
	calls = nil
	v.UpdateRoster([]Record{rec}, now.Add(time.Second))
	assertRosterCall(t, calls, "set-option", "-u", "-t", "=leo-worker:", "@leo_roster")
	assertRosterCall(t, calls, "set-option", "-u", "-t", "=leo-worker:", "status")
	if got := showSessionOption(t, run, "leo-worker", "@leo_roster"); got != "" {
		t.Fatalf("bridged roster = %q, want it cleared", got)
	}
	if got := showSessionOption(t, run, "leo-worker", "status-format[1]"); got != "" {
		t.Fatalf("bridged status-format[1] = %q, want it cleared", got)
	}

	isBridged = false
	v.UpdateRoster([]Record{rec}, now.Add(2*time.Second))
	if got := showSessionOption(t, run, "leo-worker", "@leo_roster"); !strings.Contains(got, "build") {
		t.Fatalf("roster after disconnect = %q, want the dispatch back", got)
	}
}

func TestViewerRosterKeepsDispatchesOfUnbridgedCallers(t *testing.T) {
	run, pane := isolatedTmux(t, "leo-worker")
	v := &Viewer{TmuxPath: "tmux", ExecCommand: run, BridgeConnected: func(key string) bool { return key == "worker" }}
	now := time.Now()
	bridged := Record{ID: "d-a", Kind: "dispatch", Name: "mine", Status: StatusRunning, StartedAt: now, ViewerPaneID: pane, CallerBridgeKey: "worker"}
	other := Record{ID: "d-b", Kind: "dispatch", Name: "codex-run", Status: StatusRunning, StartedAt: now, ViewerPaneID: pane}
	v.UpdateRoster([]Record{bridged, other}, now)
	got := showSessionOption(t, run, "leo-worker", "@leo_roster")
	if !strings.Contains(got, "codex-run") || strings.Contains(got, "mine") {
		t.Fatalf("roster = %q, want only the unbridged caller's dispatch", got)
	}
}

func TestViewerRosterKeepsABridgedCallersDispatchInTheDispatchSession(t *testing.T) {
	run, pane := isolatedTmux(t, dispatchViewerSession)
	v := &Viewer{TmuxPath: "tmux", ExecCommand: run, BridgeConnected: func(string) bool { return true }}
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Name: "fallback", Status: StatusRunning, StartedAt: now, ViewerPaneID: pane, CallerBridgeKey: "worker"}
	v.UpdateRoster([]Record{rec}, now)
	if got := showSessionOption(t, run, dispatchViewerSession, "@leo_roster"); !strings.Contains(got, "fallback") {
		t.Fatalf("leo-dispatch roster = %q, want the dispatch", got)
	}
}
