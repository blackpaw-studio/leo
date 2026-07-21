package tmux

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// sampleGlobalOptionsDump is a realistic (trimmed) sample of what
// `tmux -L leo show-options -g` prints on a live server that does NOT carry
// leo's foreground marker, used to prove markerState scans for the marker
// line rather than assuming any particular option ordering.
const sampleGlobalOptionsDump = `activity-action other
assume-paste-time 1
base-index 0
bell-action any
default-terminal screen
destroy-unattached off
detach-on-destroy on
display-panes-time 1000
history-limit 2000
mouse off
prefix C-b
renumber-windows off
set-titles off
status on
`

// stubServer models a fake tmux server's observable state and every argv
// leo issues against it, shared across serverExecCommand and
// startForegroundCommand so tests can assert both the outcome and the call
// order across the two seams. Guarded by a mutex since
// SuperviseForegroundServer's tests exercise it from a background goroutine
// concurrently with assertions on the test goroutine.
type stubServer struct {
	mu              sync.Mutex
	running         bool
	foreground      bool
	failShowOptions bool
	calls           []string
}

// exec resolves one serverExecCommand-shaped call against the stub's current
// state, recording the invocation and returning a real *exec.Cmd whose
// exit/stdout stands in for tmux's.
func (s *stubServer) exec(_ string, args ...string) *exec.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	case "show-options":
		if s.failShowOptions || !s.running {
			return exec.Command("false")
		}
		if s.foreground {
			return exec.Command("printf", sampleGlobalOptionsDump+"@leo-foreground 1\n")
		}
		return exec.Command("printf", sampleGlobalOptionsDump)
	case "set":
		s.foreground = true
		return exec.Command("true")
	case "kill-server":
		s.running = false
		s.foreground = false
		return exec.Command("true")
	default:
		return exec.Command("true")
	}
}

// startForeground stands in for launching `tmux -D`: it flips the stub to
// running (simulating the server having forked and started listening) and
// returns a real short-lived process so cmd.Start()/Release() have something
// concrete to operate on.
func (s *stubServer) startForeground(_ string, args ...string) *exec.Cmd {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.running = true
	s.mu.Unlock()
	return exec.Command("true")
}

// isRunning reads running under the lock, for test goroutines polling
// concurrently with the stub's own methods.
func (s *stubServer) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func withStubServer(t *testing.T, s *stubServer) {
	t.Helper()
	origExec, origStart := serverExecCommand, startForegroundCommand
	serverExecCommand = s.exec
	startForegroundCommand = s.startForeground
	t.Cleanup(func() {
		serverExecCommand = origExec
		startForegroundCommand = origStart
	})
}

func TestServerRunning(t *testing.T) {
	tests := []struct {
		name    string
		running bool
		want    bool
	}{
		{"server up", true, true},
		{"no server", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stubServer{running: tt.running}
			withStubServer(t, s)
			if got := ServerRunning("tmux"); got != tt.want {
				t.Errorf("ServerRunning() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMarkerState proves markerState correctly distinguishes a marked
// server, a genuinely unmarked one (realistic multi-line dump without the
// marker), and a failed probe — and critically that a failed probe reports
// ok=false rather than being conflated with "unmarked" (marked=false).
func TestMarkerState(t *testing.T) {
	tests := []struct {
		name       string
		running    bool
		foreground bool
		failProbe  bool
		wantMarked bool
		wantOK     bool
	}{
		{"marked foreground server", true, true, false, true, true},
		{"unmarked legacy server", true, false, false, false, true},
		{"probe error / exit nonzero", true, false, true, false, false},
		{"no server running", false, false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stubServer{running: tt.running, foreground: tt.foreground, failShowOptions: tt.failProbe}
			withStubServer(t, s)
			marked, ok := markerState("tmux")
			if marked != tt.wantMarked || ok != tt.wantOK {
				t.Errorf("markerState() = (%v, %v), want (%v, %v)", marked, ok, tt.wantMarked, tt.wantOK)
			}
		})
	}
}

// TestEnsureForegroundServerAdoptsExisting proves that when a server is
// already up and carries leo's foreground marker, EnsureForegroundServer
// adopts it without killing or restarting anything.
func TestEnsureForegroundServerAdoptsExisting(t *testing.T) {
	s := &stubServer{running: true, foreground: true}
	withStubServer(t, s)

	if err := EnsureForegroundServer("tmux"); err != nil {
		t.Fatalf("EnsureForegroundServer: %v", err)
	}
	for _, c := range s.calls {
		if strings.Contains(c, "kill-server") {
			t.Fatalf("adopt path should never kill-server, got calls: %v", s.calls)
		}
	}
	if !s.running {
		t.Fatal("adopted server should still be running")
	}
}

// TestEnsureForegroundServerAdoptsOnInconclusiveMarker is a regression guard
// for the bug where IsForegroundServer returned false on ANY probe error —
// indistinguishable from "genuinely unmarked" — causing a momentary glitch
// on leo's own (marked) server to be misread as a legacy server and
// recycled, bouncing every live agent session. When the marker probe itself
// fails but the server is still running, EnsureForegroundServer must adopt
// rather than kill.
func TestEnsureForegroundServerAdoptsOnInconclusiveMarker(t *testing.T) {
	s := &stubServer{running: true, foreground: true, failShowOptions: true}
	withStubServer(t, s)

	if err := EnsureForegroundServer("tmux"); err != nil {
		t.Fatalf("EnsureForegroundServer: %v", err)
	}
	for _, c := range s.calls {
		if strings.Contains(c, "kill-server") {
			t.Fatalf("inconclusive marker probe must never trigger a recycle, got calls: %v", s.calls)
		}
	}
	if !s.running {
		t.Fatal("expected the server to still be running after adoption")
	}
}

// TestEnsureForegroundServerRecyclesLegacyServer proves that a running but
// unmarked (legacy auto-daemonized) server is killed once and replaced with
// a fresh foreground one, in that order.
func TestEnsureForegroundServerRecyclesLegacyServer(t *testing.T) {
	s := &stubServer{running: true, foreground: false}
	withStubServer(t, s)

	if err := EnsureForegroundServer("tmux"); err != nil {
		t.Fatalf("EnsureForegroundServer: %v", err)
	}

	killIdx, startIdx := -1, -1
	for i, c := range s.calls {
		if strings.Contains(c, "kill-server") && killIdx == -1 {
			killIdx = i
		}
		if strings.HasSuffix(c, "-D") && startIdx == -1 {
			startIdx = i
		}
	}
	if killIdx == -1 {
		t.Fatalf("expected a kill-server call, got: %v", s.calls)
	}
	if startIdx == -1 {
		t.Fatalf("expected a foreground start (-D) call, got: %v", s.calls)
	}
	if killIdx > startIdx {
		t.Fatalf("kill-server must precede the fresh start, got: %v", s.calls)
	}
	if !s.running || !s.foreground {
		t.Fatal("expected the recycled server to end up running and marked foreground")
	}
}

// TestEnsureForegroundServerStartsFresh proves that with no server running
// at all, EnsureForegroundServer starts one directly without ever calling
// kill-server.
func TestEnsureForegroundServerStartsFresh(t *testing.T) {
	s := &stubServer{running: false}
	withStubServer(t, s)

	if err := EnsureForegroundServer("tmux"); err != nil {
		t.Fatalf("EnsureForegroundServer: %v", err)
	}
	for _, c := range s.calls {
		if strings.Contains(c, "kill-server") {
			t.Fatalf("fresh-start path should never kill-server, got calls: %v", s.calls)
		}
	}
	startCalled := false
	for _, c := range s.calls {
		if strings.HasSuffix(c, "-D") {
			startCalled = true
		}
	}
	if !startCalled {
		t.Fatalf("expected a foreground start (-D) call, got: %v", s.calls)
	}
	if !s.running || !s.foreground {
		t.Fatal("expected the fresh server to end up running and marked foreground")
	}
}

// TestStartForegroundServerMarksAndReleases proves a successful start polls
// until the server answers, stamps the foreground marker, and returns a
// released process handle without erroring.
func TestStartForegroundServerMarksAndReleases(t *testing.T) {
	s := &stubServer{}
	withStubServer(t, s)

	proc, err := StartForegroundServer("tmux")
	if err != nil {
		t.Fatalf("StartForegroundServer: %v", err)
	}
	if proc == nil {
		t.Fatal("expected a non-nil process handle")
	}
	if !s.foreground {
		t.Fatal("expected the server to be marked foreground after start")
	}
}

// TestStartForegroundServerTimesOut proves that a server which never starts
// answering display-message surfaces an error instead of hanging or
// silently succeeding. Uses a stub whose exec never flips running to true
// even though startForeground was invoked, simulating a tmux binary that
// fails to bind its socket.
func TestStartForegroundServerTimesOut(t *testing.T) {
	orig := serverExecCommand
	origStart := startForegroundCommand
	defer func() {
		serverExecCommand = orig
		startForegroundCommand = origStart
	}()

	// serverExecCommand always reports "not running"; startForegroundCommand
	// starts a real (short-lived) process but the stub deliberately does not
	// flip any "running" state serverExecCommand reads.
	serverExecCommand = func(_ string, args ...string) *exec.Cmd {
		if len(args) >= 3 && args[2] == "display-message" {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	startForegroundCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	origAttempts, origPoll := foregroundStartAttempts, foregroundStartPoll
	foregroundStartAttempts, foregroundStartPoll = 3, time.Millisecond
	defer func() { foregroundStartAttempts, foregroundStartPoll = origAttempts, origPoll }()

	if _, err := StartForegroundServer("tmux"); err == nil {
		t.Fatal("expected an error when the server never comes up")
	}
}

func TestEnsureForegroundServerPropagatesStartFailure(t *testing.T) {
	orig := serverExecCommand
	origStart := startForegroundCommand
	defer func() {
		serverExecCommand = orig
		startForegroundCommand = origStart
	}()

	serverExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("false")
	}
	startForegroundCommand = func(_ string, _ ...string) *exec.Cmd {
		// A command that fails to even start.
		return exec.Command("/nonexistent/definitely-not-a-binary")
	}

	if err := EnsureForegroundServer("tmux"); err == nil {
		t.Fatal("expected EnsureForegroundServer to surface the start failure")
	}
}

// TestSuperviseForegroundServerRestartsOnExit proves the monitor loop
// notices a dead server and restarts it, and that it stops polling (without
// killing the now-running server) once ctx is cancelled.
func TestSuperviseForegroundServerRestartsOnExit(t *testing.T) {
	s := &stubServer{running: false}
	withStubServer(t, s)
	origInterval := foregroundSuperviseInterval
	foregroundSuperviseInterval = 5 * time.Millisecond
	defer func() { foregroundSuperviseInterval = origInterval }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SuperviseForegroundServer(ctx, "tmux")
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !s.isRunning() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !s.isRunning() {
		t.Fatal("expected SuperviseForegroundServer to restart the dead server")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SuperviseForegroundServer did not stop after ctx cancellation")
	}
}
