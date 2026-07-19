package tmux

import (
	"context"
	"os/exec"
	"testing"
)

// TestResolvePaneReturnsLowestNumberedPaneID proves ResolvePane picks the
// server-global lowest-numbered pane ID out of an unordered multi-pane
// listing — the original pane the harness was started in, even if later
// splits created higher-numbered panes and moved the active pane away from
// it.
func TestResolvePaneReturnsLowestNumberedPaneID(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "%12\n%3\n%25\n")
	}

	got, err := ResolvePane(context.Background(), "tmux", "leo-agent-foo")
	if err != nil {
		t.Fatalf("ResolvePane: %v", err)
	}
	if want := "%3"; got != want {
		t.Fatalf("ResolvePane = %q, want %q", got, want)
	}
}

// TestResolvePaneSinglePane proves a session with only one pane returns that
// pane's ID unchanged.
func TestResolvePaneSinglePane(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "%7\n")
	}

	got, err := ResolvePane(context.Background(), "tmux", "leo-agent-foo")
	if err != nil {
		t.Fatalf("ResolvePane: %v", err)
	}
	if want := "%7"; got != want {
		t.Fatalf("ResolvePane = %q, want %q", got, want)
	}
}

// TestResolvePaneEmptyOutputErrors proves an empty list-panes output (no
// panes found, or the session doesn't exist) is an error rather than a
// silently empty target.
func TestResolvePaneEmptyOutputErrors(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}

	if _, err := ResolvePane(context.Background(), "tmux", "leo-agent-foo"); err == nil {
		t.Fatal("expected error for empty list-panes output, got nil")
	}
}

// TestResolvePaneGarbageLineErrors proves an unparsable pane-id line (not a
// "%N" form) is an error rather than being silently skipped or accepted as a
// target.
func TestResolvePaneGarbageLineErrors(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "not-a-pane-id\n")
	}

	if _, err := ResolvePane(context.Background(), "tmux", "leo-agent-foo"); err == nil {
		t.Fatal("expected error for unparsable pane id line, got nil")
	}
}

// TestLowestPaneIDReturnsLowestNumber proves LowestPaneID — the parsing core
// ResolvePane delegates to — picks the lowest numeric pane id out of
// unordered multi-line list-panes output. Exported so callers with their own
// exec seam (e.g. package web, whose Server.execCommand doesn't take a
// context) can run list-panes themselves and reuse this selection logic
// instead of duplicating it.
func TestLowestPaneIDReturnsLowestNumber(t *testing.T) {
	got, err := LowestPaneID("%12\n%3\n%25\n")
	if err != nil {
		t.Fatalf("LowestPaneID: %v", err)
	}
	if want := "%3"; got != want {
		t.Fatalf("LowestPaneID = %q, want %q", got, want)
	}
}

// TestLowestPaneIDEmptyErrors proves an empty listing is an error rather than
// a silently empty pane id.
func TestLowestPaneIDEmptyErrors(t *testing.T) {
	if _, err := LowestPaneID(""); err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// TestLowestPaneIDGarbageErrors proves an unparsable line is an error rather
// than being silently skipped or accepted as a pane id.
func TestLowestPaneIDGarbageErrors(t *testing.T) {
	if _, err := LowestPaneID("not-a-pane-id\n"); err == nil {
		t.Fatal("expected error for unparsable pane id line, got nil")
	}
}

// TestResolvePaneCommandErrorPropagates proves a failing list-panes command
// (e.g. the session doesn't exist) surfaces as an error rather than being
// swallowed.
func TestResolvePaneCommandErrorPropagates(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	if _, err := ResolvePane(context.Background(), "tmux", "leo-agent-foo"); err == nil {
		t.Fatal("expected error when list-panes command fails, got nil")
	}
}
