package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// reparentHarness builds a reparentDeps with sane defaults plus recorders, so
// each test overrides only what it exercises.
type reparentHarness struct {
	deps     reparentDeps
	killed   bool
	prompted string
	out      bytes.Buffer
}

func newReparentHarness(own tmux.Ownership) *reparentHarness {
	h := &reparentHarness{}
	h.deps = reparentDeps{
		daemonRunning: func() bool { return true },
		locate:        func() (string, error) { return "tmux", nil },
		serverRunning: func(string) bool { return true },
		ownership:     func(string) (tmux.Ownership, error) { return own, nil },
		killServer: func(string) error {
			h.killed = true
			return nil
		},
		counts:  func() (int, int) { return 13, 25 },
		confirm: func(msg string) bool { h.prompted = msg; return true },
		waitForOwner: func(string) (tmux.Ownership, bool) {
			return tmux.Ownership{ServerPID: 99001, Stamped: true, OwnerPID: 99000, OwnerLive: true}, true
		},
		out: &h.out,
	}
	return h
}

func TestReparentRequiresRunningDaemon(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{})
	h.deps.daemonRunning = func() bool { return false }

	err := reparentServer(h.deps)
	if err == nil {
		t.Fatal("expected an error when the daemon is not running")
	}
	if !strings.Contains(err.Error(), "leo service start") {
		t.Fatalf("error %q should point at starting the daemon", err)
	}
	if h.killed {
		t.Fatal("must not kill the server with no daemon to respawn it")
	}
}

func TestReparentNoOpWhenNoServer(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{})
	h.deps.serverRunning = func(string) bool { return false }

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if h.killed {
		t.Fatal("must not kill a server that isn't running")
	}
	if !strings.Contains(h.out.String(), "no tmux server") {
		t.Fatalf("output %q should explain there was nothing to do", h.out.String())
	}
}

// The repair is destructive, so it declines to run when the invariant is
// already intact.
func TestReparentSkipsHealthyServer(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 33666, OwnerLive: true})

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if h.killed {
		t.Fatal("must not recycle a server whose owner is alive")
	}
	if !strings.Contains(h.out.String(), "--force") {
		t.Fatalf("output %q should mention how to override", h.out.String())
	}
}

func TestReparentForceRecyclesHealthyServer(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 33666, OwnerLive: true})
	h.deps.force = true

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if !h.killed {
		t.Fatal("--force should recycle even a healthy server")
	}
}

func TestReparentConfirmationNamesTheCost(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if !strings.Contains(h.prompted, "13") || !strings.Contains(h.prompted, "25") {
		t.Fatalf("prompt %q must state how many sessions are affected", h.prompted)
	}
}

// serverRunning already confirmed a server is up, so an ownership-probe
// failure must not be reported as "no tmux server" — that would be a flatly
// false statement to the operator immediately before a destructive step.
func TestReparentDoesNotDenyServerWhenOwnershipProbeFails(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{})
	h.deps.ownership = func(string) (tmux.Ownership, error) {
		return tmux.Ownership{}, fmt.Errorf("reading tmux global options: exit status 1")
	}

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if strings.Contains(h.out.String(), "no tmux server") {
		t.Fatalf("output %q must not claim there is no server; one was just confirmed running", h.out.String())
	}
	if !strings.Contains(h.out.String(), "ownership") {
		t.Fatalf("output %q should say the ownership probe failed", h.out.String())
	}
	// An unreadable owner is not a verified owner, so it must still confirm
	// and proceed rather than silently skipping the repair.
	if !h.killed {
		t.Fatal("should still offer the repair when ownership is unknown")
	}
}

func TestReparentDeclinedDoesNotKill(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})
	h.deps.confirm = func(string) bool { return false }

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if h.killed {
		t.Fatal("declining the prompt must not kill the server")
	}
	if !strings.Contains(h.out.String(), "Aborted") {
		t.Fatalf("output %q should say it aborted", h.out.String())
	}
}

func TestReparentYesSkipsPrompt(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})
	h.deps.assumeYes = true
	h.deps.confirm = func(string) bool {
		t.Fatal("--yes must not prompt")
		return false
	}

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	if !h.killed {
		t.Fatal("--yes should proceed with the recycle")
	}
}

func TestReparentReportsNewOwner(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})

	if err := reparentServer(h.deps); err != nil {
		t.Fatalf("reparentServer: %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "99001") || !strings.Contains(out, "99000") {
		t.Fatalf("output %q should report the new server and owner pids", out)
	}
}

func TestReparentErrorsWhenServerDoesNotComeBack(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})
	h.deps.waitForOwner = func(string) (tmux.Ownership, bool) { return tmux.Ownership{}, false }

	err := reparentServer(h.deps)
	if err == nil {
		t.Fatal("expected an error when no owned server comes back")
	}
	if !strings.Contains(err.Error(), "leo service restart") {
		t.Fatalf("error %q should offer the next step", err)
	}
}

func TestReparentSurfacesKillFailure(t *testing.T) {
	h := newReparentHarness(tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688})
	h.deps.killServer = func(string) error { return fmt.Errorf("boom") }

	if err := reparentServer(h.deps); err == nil {
		t.Fatal("expected the kill failure to surface")
	}
}

func TestDescribeTmuxTree(t *testing.T) {
	tests := []struct {
		name          string
		own           tmux.Ownership
		err           error
		wantContains  string
		wantVerified  bool
		wantSrvExists bool
	}{
		{
			name:          "live owner",
			own:           tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 33666, OwnerLive: true},
			wantContains:  "owned by live leo pid 33666",
			wantVerified:  true,
			wantSrvExists: true,
		},
		{
			name:          "adopted orphan",
			own:           tmux.Ownership{ServerPID: 32086, Stamped: true, OwnerPID: 58688},
			wantContains:  "creating leo process (pid 58688) has exited",
			wantVerified:  false,
			wantSrvExists: true,
		},
		{
			name:          "unstamped legacy server",
			own:           tmux.Ownership{ServerPID: 71009},
			wantContains:  "owner unknown",
			wantVerified:  false,
			wantSrvExists: true,
		},
		{
			name:          "no server",
			err:           fmt.Errorf("reading tmux global options: exit status 1"),
			wantContains:  "no tmux server",
			wantVerified:  false,
			wantSrvExists: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := describeTmuxTree(tc.own, tc.err)
			if !strings.Contains(got.Line, tc.wantContains) {
				t.Errorf("Line = %q, want it to contain %q", got.Line, tc.wantContains)
			}
			if got.OwnerVerified != tc.wantVerified {
				t.Errorf("OwnerVerified = %v, want %v", got.OwnerVerified, tc.wantVerified)
			}
			if got.ServerPresent != tc.wantSrvExists {
				t.Errorf("ServerPresent = %v, want %v", got.ServerPresent, tc.wantSrvExists)
			}
		})
	}
}
