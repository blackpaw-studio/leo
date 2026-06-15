package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestArgsPrependsSocket(t *testing.T) {
	got := Args("new-session", "-d", "-s", "foo")
	want := []string{"-L", "leo", "new-session", "-d", "-s", "foo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args(...) = %v, want %v", got, want)
	}
}

func TestArgsEmpty(t *testing.T) {
	got := Args()
	want := []string{"-L", "leo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args() = %v, want %v", got, want)
	}
}

func TestArgsDoesNotAliasInput(t *testing.T) {
	in := []string{"kill-session", "-t", "x"}
	got := Args(in...)
	got[0] = "mutated"
	if in[0] != "kill-session" {
		t.Errorf("Args mutated caller's slice backing array; in[0] = %q", in[0])
	}
}

func TestSocketNameIsLeo(t *testing.T) {
	if SocketName != "leo" {
		t.Errorf("SocketName = %q, want %q", SocketName, "leo")
	}
}

func TestTargetExactMatchPrefix(t *testing.T) {
	if got, want := Target("leo-leo"), "=leo-leo"; got != want {
		t.Fatalf("Target(%q) = %q, want %q", "leo-leo", got, want)
	}
}

// TestTargetAvoidsPrefixCollision proves, against real tmux, that wrapping a
// session name with Target prevents the prefix-matching misfire behind the
// "vanishing agent" bug: a has-session probe for "leo-leo" must NOT match an
// existing "leo-leoterm" session.
func TestTargetAvoidsPrefixCollision(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	// Use a private, test-scoped socket so we never touch the real "leo"
	// daemon socket. Keyed by PID to avoid cross-run collisions.
	sock := fmt.Sprintf("leo-test-target-%d", os.Getpid())
	run := func(args ...string) error {
		return exec.Command(tmuxPath, append([]string{"-L", sock}, args...)...).Run()
	}
	defer func() { _ = run("kill-server") }()

	if err := run("new-session", "-d", "-s", "leo-leoterm", "sleep 120"); err != nil {
		t.Fatalf("new-session leo-leoterm: %v", err)
	}

	// Sanity: the bare-name probe prefix-matches leo-leoterm (the bug). If a
	// given tmux build doesn't prefix-match, the regression isn't meaningful
	// here, so skip rather than assert the bug exists.
	if err := run("has-session", "-t", "leo-leo"); err != nil {
		t.Skip("tmux build does not prefix-match bare targets; nothing to guard")
	}

	// The fix: the exact-match target must NOT resolve to leo-leoterm.
	if err := run("has-session", "-t", Target("leo-leo")); err == nil {
		t.Fatalf("has-session -t %q matched leo-leoterm; exact match should fail", Target("leo-leo"))
	}
}

func TestPaneTargetExactMatchForm(t *testing.T) {
	if got, want := PaneTarget("leo-leo"), "=leo-leo:"; got != want {
		t.Fatalf("PaneTarget(%q) = %q, want %q", "leo-leo", got, want)
	}
}

// TestPaneTargetResolvesExactAndAvoidsPrefix proves, against real tmux, that
// PaneTarget is the correct exact form for pane commands (capture-pane etc.):
// it resolves the session it names exactly, and does NOT prefix-match a longer
// sibling. A bare "=name" (Target) does not resolve as a pane at all, so pane
// commands must use PaneTarget.
func TestPaneTargetResolvesExactAndAvoidsPrefix(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("leo-test-panetarget-%d", os.Getpid())
	run := func(args ...string) error {
		return exec.Command(tmuxPath, append([]string{"-L", sock}, args...)...).Run()
	}
	defer func() { _ = run("kill-server") }()

	if err := run("new-session", "-d", "-s", "leo-leoterm", "sleep 120"); err != nil {
		t.Fatalf("new-session leo-leoterm: %v", err)
	}

	// PaneTarget resolves the exact session it names.
	if err := run("capture-pane", "-p", "-t", PaneTarget("leo-leoterm")); err != nil {
		t.Fatalf("capture-pane -t %q should resolve existing session: %v", PaneTarget("leo-leoterm"), err)
	}
	// PaneTarget for a non-existent prefix must NOT match leo-leoterm.
	if err := run("capture-pane", "-p", "-t", PaneTarget("leo-leo")); err == nil {
		t.Fatalf("capture-pane -t %q matched leo-leoterm; exact match should fail", PaneTarget("leo-leo"))
	}
}
