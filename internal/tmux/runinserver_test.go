package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runStub models a fake tmux server for RunInServer: it records every argv and
// simulates the pane command by writing whatever the caller redirected into the
// capture file, so tests can assert the captured output round-trips.
type runStub struct {
	running bool
	calls   []string
	// paneOutput is written to the capture file when new-session is issued,
	// standing in for what the pane command would have printed.
	paneOutput string
	// neverExits makes has-session keep reporting the probe session alive, so
	// RunInServer has to hit its timeout.
	neverExits  bool
	sessionGone bool
	failNew     bool
}

func (s *runStub) exec(_ string, args ...string) *exec.Cmd {
	s.calls = append(s.calls, strings.Join(args, " "))
	sub := ""
	if len(args) >= 3 {
		sub = args[2]
	}
	switch sub {
	case "display-message":
		if s.running {
			return exec.Command("echo", "1234")
		}
		return exec.Command("false")
	case "new-session":
		if s.failNew {
			return exec.Command("sh", "-c", "echo 'no server running' >&2; exit 1")
		}
		// Emulate the pane command's side effect: the shell command leo
		// passes redirects output into the capture file, whose path is the
		// last thing mentioned in the argv.
		if path := captureFileFrom(args); path != "" && s.paneOutput != "" {
			_ = os.WriteFile(path, []byte(s.paneOutput), 0o600)
		}
		if !s.neverExits {
			s.sessionGone = true
		}
		return exec.Command("true")
	case "has-session":
		if s.sessionGone {
			return exec.Command("false")
		}
		return exec.Command("true")
	default:
		return exec.Command("true")
	}
}

// captureFileFrom digs the capture-file path out of a recorded new-session
// argv by looking for the token containing the temp-file marker.
func captureFileFrom(args []string) string {
	for _, a := range args {
		if idx := strings.Index(a, "leo-doctor-probe-"); idx >= 0 {
			for _, field := range strings.Fields(a) {
				if strings.Contains(field, "leo-doctor-probe-") && strings.Contains(field, string(os.PathSeparator)) {
					return strings.Trim(field, "'\"")
				}
			}
		}
	}
	return ""
}

func withRunStub(t *testing.T, s *runStub) {
	t.Helper()
	origExec := serverExecCommand
	origPoll := probePoll
	serverExecCommand = s.exec
	probePoll = time.Millisecond
	t.Cleanup(func() {
		serverExecCommand = origExec
		probePoll = origPoll
	})
}

func TestRunInServerReturnsPaneOutput(t *testing.T) {
	stub := &runStub{running: true, paneOutput: "LAN OK\n"}
	withRunStub(t, stub)

	out, err := RunInServer("tmux", "echo LAN OK", time.Second)
	if err != nil {
		t.Fatalf("RunInServer: %v", err)
	}
	if strings.TrimSpace(out) != "LAN OK" {
		t.Fatalf("output = %q, want %q", out, "LAN OK")
	}
}

func TestRunInServerFailsWhenNoServerRunning(t *testing.T) {
	stub := &runStub{running: false}
	withRunStub(t, stub)

	if _, err := RunInServer("tmux", "echo hi", time.Second); err == nil {
		t.Fatal("expected an error when no tmux server is running")
	}
	for _, c := range stub.calls {
		if strings.Contains(c, "new-session") {
			t.Fatalf("must not create a session when no server is running; calls: %v", stub.calls)
		}
	}
}

func TestRunInServerKillsSessionOnTimeout(t *testing.T) {
	stub := &runStub{running: true, neverExits: true}
	withRunStub(t, stub)

	_, err := RunInServer("tmux", "sleep 60", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if !hasCall(stub.calls, "kill-session") {
		t.Fatalf("probe session must be killed after a timeout; calls: %v", stub.calls)
	}
}

func TestRunInServerKillsSessionOnSuccess(t *testing.T) {
	stub := &runStub{running: true, paneOutput: "done\n"}
	withRunStub(t, stub)

	if _, err := RunInServer("tmux", "echo done", time.Second); err != nil {
		t.Fatalf("RunInServer: %v", err)
	}
	if !hasCall(stub.calls, "kill-session") {
		t.Fatalf("probe session must be killed after a successful run; calls: %v", stub.calls)
	}
}

// The probe session must not land in the agent namespace: leo's agent sessions
// are "leo-<name>", and a probe named that way could be mistaken for an agent
// by anything enumerating sessions.
func TestRunInServerUsesNonAgentSessionName(t *testing.T) {
	stub := &runStub{running: true, paneOutput: "x\n"}
	withRunStub(t, stub)

	if _, err := RunInServer("tmux", "echo x", time.Second); err != nil {
		t.Fatalf("RunInServer: %v", err)
	}

	var newSession string
	for _, c := range stub.calls {
		if strings.Contains(c, "new-session") {
			newSession = c
		}
	}
	if newSession == "" {
		t.Fatalf("no new-session call recorded: %v", stub.calls)
	}
	if !strings.Contains(newSession, "leo-doctor-probe-") {
		t.Fatalf("new-session argv %q must use the doctor-probe session prefix", newSession)
	}
}

func TestRunInServerCleansUpCaptureFile(t *testing.T) {
	stub := &runStub{running: true, paneOutput: "y\n"}
	withRunStub(t, stub)

	if _, err := RunInServer("tmux", "echo y", time.Second); err != nil {
		t.Fatalf("RunInServer: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "leo-doctor-probe-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("capture file(s) left behind: %v", matches)
	}
}

func TestRunInServerSurfacesNewSessionFailure(t *testing.T) {
	stub := &runStub{running: true, failNew: true}
	withRunStub(t, stub)

	_, err := RunInServer("tmux", "echo z", time.Second)
	if err == nil {
		t.Fatal("expected an error when new-session fails")
	}
	if !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("error = %v, want tmux's stderr included", err)
	}
}

func hasCall(calls []string, sub string) bool {
	for _, c := range calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}
