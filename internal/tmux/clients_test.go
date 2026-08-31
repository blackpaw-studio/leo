package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestHasAttachedClientTrueWhenListClientsReturnsOutput proves a non-empty
// `list-clients` result reports the session as attached, and that the exact
// argv leo issues is `-L leo list-clients -t =<session>` — the exact-match
// target form, not a bare (prefix-matchable) session name.
func TestHasAttachedClientTrueWhenListClientsReturnsOutput(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	var gotArgs []string
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("printf", "%s", "/dev/ttys001 leo-user 12345\n")
	}

	if !HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = false, want true for non-empty list-clients output")
	}

	want := []string{"-L", "leo", "list-clients", "-t", "=leo-agent-foo"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", gotArgs, want)
	}
}

// TestHasAttachedClientFalseWhenListClientsEmpty proves an empty (but
// successful) list-clients result — the normal case for a session nobody has
// attached to — reports false.
func TestHasAttachedClientFalseWhenListClientsEmpty(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}

	if HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = true, want false for empty list-clients output")
	}
}

// TestHasAttachedClientFalseOnError proves a failing list-clients invocation
// (e.g. the session doesn't exist) fails open to false rather than false
// positive-ing an attach, since callers treat "false" as "safe to
// auto-dismiss".
func TestHasAttachedClientFalseOnError(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	if HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = true, want false when the command errors")
	}
}
