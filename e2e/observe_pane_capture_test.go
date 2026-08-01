//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// TestObserveTrackerCapturesPaneActionFromRealTmux exercises
// observe.Tracker against a REAL tmux server, not the fakeCaptureCommand
// seam internal/observe's own unit tests use. That seam replaces
// captureExecCommand's *arguments* along with its output, so an argv-syntax
// regression (a target-SESSION passed where tmux capture-pane requires a
// target-PANE — the production bug this test guards) is invisible to it.
// Only a real tmux server can fail a bad `-t` argument the way production
// tmux does.
func TestObserveTrackerCapturesPaneActionFromRealTmux(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available; skipping live pane-capture test")
	}

	const agentName = "e2epanecapture"
	sessionName := agent.SessionName(agentName)

	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run()
	})
	if hasTmuxSession(tmuxPath, sessionName) {
		t.Fatalf("precondition failed: session %q already exists", sessionName)
	}

	const knownText = "OBSERVE-PANE-CAPTURE-FIXTURE"
	// A short-lived shell that prints known text once and then idles, so the
	// pane has stable, capturable content for the sweep to sample. $1 (not a
	// literal %s) is how sh -c substitutes the trailing positional arg.
	newSession := exec.Command(tmuxPath, tmux.Args(
		"new-session", "-d", "-s", sessionName,
		"sh", "-c", `printf '%s\n' "$1"; sleep 30`, "_", knownText,
	)...)
	if out, err := newSession.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}

	tr := observe.NewTracker(tmuxPath, func() map[string]string {
		return map[string]string{agentName: sessionName}
	}, nil, observe.WithSweepInterval(100*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go tr.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	var gotDetail string
	for time.Now().Before(deadline) {
		if act, ok := tr.Activities()[agentName]; ok && act.CurrentAction != nil {
			gotDetail = act.CurrentAction.Detail
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if gotDetail != knownText {
		t.Fatalf("expected CurrentAction.Detail %q from real tmux capture-pane, got %q", knownText, gotDetail)
	}
}
