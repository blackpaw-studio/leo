package tmux

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// paneWithInput renders a minimal claude-pane snapshot whose input line carries
// the given text (empty text = an idle prompt).
func paneWithInput(text string) string {
	return "──────── border ────────\n❯ " + text + "\n──────── border ────────\n  [Sonnet 4.6] | high\n  Session: 6.0%\n"
}

// countSub counts recorded tmux calls whose subcommand (after "-L","leo") matches.
func countSub(got [][]string, sub string) int {
	n := 0
	for _, c := range got {
		if len(c) >= 4 && c[3] == sub {
			n++
		}
	}
	return n
}

// firstSub returns the first recorded tmux call whose subcommand matches.
func firstSub(got [][]string, sub string) []string {
	for _, c := range got {
		if len(c) >= 4 && c[3] == sub {
			return c
		}
	}
	return nil
}

func TestInjectPromptCalls(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		// Warm path: claude is ready, so the readiness probe is echoed into the
		// input box on the first capture.
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", paneWithInput(inputProbe))
		}
		return exec.Command("true")
	}
	if err := InjectPrompt(context.Background(), "tmux", "leo-session-foo", "hello\nworld"); err != nil {
		t.Fatalf("InjectPrompt: %v", err)
	}
	// The body must be staged and pasted exactly once (never stacked).
	if n := countSub(got, "set-buffer"); n != 1 {
		t.Fatalf("expected exactly 1 set-buffer, got %d: %#v", n, got)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("expected exactly 1 paste-buffer, got %d: %#v", n, got)
	}
	expectSet := []string{"tmux", "-L", "leo", "set-buffer", "-b", "leo-leo-session-foo", "--", "hello\nworld"}
	expectPaste := []string{"tmux", "-L", "leo", "paste-buffer", "-b", "leo-leo-session-foo", "-t", "=leo-session-foo:", "-d"}
	if c := firstSub(got, "set-buffer"); !reflect.DeepEqual(c, expectSet) {
		t.Fatalf("set-buffer call wrong:\n got %#v\nwant %#v", c, expectSet)
	}
	if c := firstSub(got, "paste-buffer"); !reflect.DeepEqual(c, expectPaste) {
		t.Fatalf("paste-buffer call wrong:\n got %#v\nwant %#v", c, expectPaste)
	}
	// The submit Enter must be the final call.
	last := got[len(got)-1]
	expectEnter := []string{"tmux", "-L", "leo", "send-keys", "-t", "=leo-session-foo:", "Enter"}
	if !reflect.DeepEqual(last, expectEnter) {
		t.Fatalf("last call must be submit Enter:\n got %#v\nwant %#v", last, expectEnter)
	}
}

// TestInjectPromptProbesUntilReady proves InjectPrompt waits for claude to start
// accepting input (probe echoes back) before pasting the body — and pastes the
// body exactly once even across multiple probe attempts. This guards the
// cold-start case where a freshly booted claude silently drops early input.
func TestInjectPromptProbesUntilReady(t *testing.T) {
	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			// First two probes dropped (box empty); third probe registers.
			if captureCalls < 3 {
				return exec.Command("printf", "%s", paneWithInput(""))
			}
			return exec.Command("printf", "%s", paneWithInput(inputProbe))
		}
		return exec.Command("true")
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body\nlines", 10, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}

	// Probed more than once (retried while claude booted).
	probes := 0
	for _, c := range got {
		if len(c) >= 8 && c[3] == "send-keys" && c[6] == "-l" && c[7] == inputProbe {
			probes++
		}
	}
	if probes < 2 {
		t.Fatalf("expected >=2 readiness probes, got %d: %#v", probes, got)
	}
	// Body pasted exactly once regardless of probe retries.
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	// Final call is the submit Enter.
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
	}
}

// TestInjectPromptFailsWhenNeverReady verifies a bounded give-up: if the input
// box is present but never accepts the probe, InjectPrompt errors and does NOT
// paste the body or submit (which would hang until the task timeout).
func TestInjectPromptFailsWhenNeverReady(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", paneWithInput("")) // box present, probe never lands
		}
		return exec.Command("true")
	}
	err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body", 3, time.Millisecond)
	if err == nil {
		t.Fatal("expected error when claude never accepts input, got nil")
	}
	if n := countSub(got, "paste-buffer"); n != 0 {
		t.Fatalf("must not paste body when never ready, got %d paste-buffer calls", n)
	}
	for _, c := range got {
		if len(c) >= 5 && c[3] == "send-keys" && c[len(c)-1] == "Enter" {
			t.Fatal("must not submit Enter when never ready")
		}
	}
}

// TestInjectPromptFallsOpenWhenInputBoxUnrecognized verifies that when the pane
// never shows a recognizable claude input box (unexpected TUI format, not a
// boot delay), InjectPrompt pastes and submits anyway rather than failing — so
// the readiness gate can never make a working session worse than before.
func TestInjectPromptFallsOpenWhenInputBoxUnrecognized(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", "some unrecognized pane\nno prompt glyph anywhere\n")
		}
		return exec.Command("true")
	}
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body", 3, time.Millisecond); err != nil {
		t.Fatalf("expected fall-open (nil error), got %v", err)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("expected body pasted once on fall-open, got %d", n)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("expected submit Enter on fall-open, got %#v", last)
	}
}

func TestClassifyInput(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want inputState
	}{
		{"empty input box", paneWithInput(""), inputEmpty},
		{"input with text", paneWithInput("do the thing"), inputHasContent},
		{"input is last prompt line, ignores quoted scrollback above", "> a markdown quote in history\n──── border ────\n❯ real prompt\n──── border ────\n  [Sonnet]", inputHasContent},
		{"no prompt line at all", "just some boot output\nno glyph here\n", inputUnknown},
		{"empty input below a prior response", "⏺ PONG\n──── border ────\n❯ \n──── border ────\n  Session: 5%", inputEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyInput(tt.pane); got != tt.want {
				t.Fatalf("classifyInput = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAbortPromptCalls(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		return exec.Command("true")
	}
	if err := AbortPrompt(context.Background(), "tmux", "leo-session-foo"); err != nil {
		t.Fatalf("AbortPrompt: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(got))
	}
	expectEscape := []string{"tmux", "-L", "leo", "send-keys", "-t", "=leo-session-foo:", "Escape"}
	expectCtrlC := []string{"tmux", "-L", "leo", "send-keys", "-t", "=leo-session-foo:", "C-c"}
	if !reflect.DeepEqual(got[0], expectEscape) {
		t.Fatalf("Escape call wrong:\n got %#v\nwant %#v", got[0], expectEscape)
	}
	if !reflect.DeepEqual(got[1], expectCtrlC) {
		t.Fatalf("C-c call wrong:\n got %#v\nwant %#v", got[1], expectCtrlC)
	}
}
