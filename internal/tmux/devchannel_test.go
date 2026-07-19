package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// requireTmux skips a test if tmux is not available locally. Exposed here so
// the rest of the tmux package can share it without build-tag trickery.
func requireTmux(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available; skipping live tmux test")
	}
	return path
}

// uniqueSession returns a session name unique enough to avoid collisions with
// other concurrent tests. tmux treats '.' as a window separator so the suffix
// uses only digits.
func uniqueSession(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestAcceptDevChannelPromptRejectsEmptyArgs(t *testing.T) {
	if err := AcceptDevChannelPrompt(context.Background(), "", "s"); err == nil {
		t.Error("expected error for empty tmux path")
	}
	if err := AcceptDevChannelPrompt(context.Background(), "/bin/true", ""); err == nil {
		t.Error("expected error for empty session name")
	}
}

// TestAcceptDevChannelPromptSuccess launches a real tmux session that prints
// the prompt marker, calls the accepter, and verifies the pane receives the
// Enter keystroke by observing a sentinel file that our fake "prompt" writes
// when it gets input.
func TestAcceptDevChannelPromptSuccess(t *testing.T) {
	tmuxPath := requireTmux(t)

	session := uniqueSession("leo-devchan-test")
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, Args("kill-session", "-t", session)...).Run()
	})

	// A tiny shell snippet that prints the marker, then `read`s a line from
	// the pane stdin and exits. `send-keys Enter` produces a newline, so the
	// read returns and the shell exits cleanly.
	script := `echo "WARNING: Loading development channels"; echo ""; echo "I am using this for local development"; read dummy; echo "ACCEPTED"; sleep 1`

	createCmd := exec.Command(tmuxPath, Args(
		"new-session", "-d", "-s", session,
		"-x", "120", "-y", "30",
		"sh", "-c", script,
	)...,
	)
	if err := createCmd.Run(); err != nil {
		t.Fatalf("failed to create tmux session: %v", err)
	}

	// Give tmux a brief moment so the `echo` has flushed before we start polling.
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := acceptDevChannelPrompt(ctx, tmuxPath, session, 5*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("AcceptDevChannelPrompt: %v", err)
	}

	// After Enter is sent, the shell should print ACCEPTED. Poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command(tmuxPath, Args("capture-pane", "-p", "-t", session)...).Output()
		if err == nil && contains(string(out), "ACCEPTED") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("pane never showed ACCEPTED — Enter did not reach the session")
}

func TestAcceptDevChannelPromptTimeout(t *testing.T) {
	tmuxPath := requireTmux(t)

	session := uniqueSession("leo-devchan-timeout")
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, Args("kill-session", "-t", session)...).Run()
	})

	// Session with no prompt text — the accepter should time out.
	createCmd := exec.Command(tmuxPath, Args(
		"new-session", "-d", "-s", session,
		"-x", "120", "-y", "30",
		"sh", "-c", "sleep 10",
	)...,
	)
	if err := createCmd.Run(); err != nil {
		t.Fatalf("failed to create tmux session: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := acceptDevChannelPrompt(ctx, tmuxPath, session, 500*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error when prompt never appears")
	}
}

// TestAcceptDevChannelPromptUsesResolvedPaneTarget proves the accepter
// targets the concrete pane ResolvePane reports, not PaneTarget's
// active-pane selector — so a split session doesn't misdirect the
// dev-channel accept.
func TestAcceptDevChannelPromptUsesResolvedPaneTarget(t *testing.T) {
	const resolvedPane = "%17"
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if isListPanes(args) {
			return exec.Command("printf", "%s", paneListOutput(resolvedPane))
		}
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", devChannelPromptMarker+"\n")
		}
		return exec.Command("true")
	}

	if err := acceptDevChannelPrompt(context.Background(), "tmux", "leo-agent-foo", time.Second, time.Millisecond); err != nil {
		t.Fatalf("acceptDevChannelPrompt: %v", err)
	}

	for _, sub := range []string{"capture-pane", "send-keys"} {
		found := false
		for _, c := range got {
			if len(c) < 4 || c[3] != sub {
				continue
			}
			found = true
			target := ""
			for i, a := range c {
				if a == "-t" && i+1 < len(c) {
					target = c[i+1]
					break
				}
			}
			if target != resolvedPane {
				t.Fatalf("%s call targeted %q, want resolved pane %q: %#v", sub, target, resolvedPane, c)
			}
		}
		if !found {
			t.Fatalf("expected at least one %s call, got none: %#v", sub, got)
		}
	}
}

// TestAcceptDevChannelPromptFallsBackWhenResolveFails proves the accepter
// stays best-effort: when ResolvePane can't be resolved, it falls back to
// PaneTarget's active-pane selector rather than erroring louder than before
// ResolvePane existed.
func TestAcceptDevChannelPromptFallsBackWhenResolveFails(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if isListPanes(args) {
			return exec.Command("false")
		}
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", devChannelPromptMarker+"\n")
		}
		return exec.Command("true")
	}

	if err := acceptDevChannelPrompt(context.Background(), "tmux", "leo-agent-foo", time.Second, time.Millisecond); err != nil {
		t.Fatalf("acceptDevChannelPrompt: %v", err)
	}

	wantTarget := PaneTarget("leo-agent-foo")
	found := false
	for _, c := range got {
		if len(c) >= 4 && c[3] == "send-keys" {
			found = true
			target := ""
			for i, a := range c {
				if a == "-t" && i+1 < len(c) {
					target = c[i+1]
					break
				}
			}
			if target != wantTarget {
				t.Fatalf("send-keys call targeted %q, want fallback %q: %#v", target, wantTarget, c)
			}
		}
	}
	if !found {
		t.Fatal("expected a send-keys Enter call, got none")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
