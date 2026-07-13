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
		// input box on the first capture. The pane also already carries the
		// body, mirroring real claude's synchronous paste render, so phase
		// 3's confirm loop breaks on its first capture instead of exercising
		// the full budget-expiry fallback.
		if len(args) >= 3 && args[2] == "capture-pane" {
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"hello\nworld\n")
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
			// First two probes dropped (box empty); third probe registers,
			// and the pane already carries the body so phase 3's confirm
			// loop breaks immediately (real claude renders synchronously).
			if captureCalls < 3 {
				return exec.Command("printf", "%s", paneWithInput(""))
			}
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"body\nlines\n")
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

// TestInjectPromptWaitsForLateSession proves the readiness probe tolerates a
// session that does not exist yet — a just-resumed idle-suspended agent whose
// tmux new-session lags the spawn call (which registers state + starts the
// supervise goroutine asynchronously). The probe send-keys fails until the
// session appears, then InjectPrompt proceeds to paste once and submit, rather
// than aborting on the first failure (the live auto-wake "can't find session"
// bug).
func TestInjectPromptWaitsForLateSession(t *testing.T) {
	var got [][]string
	probeSendKeys := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		// Probe send-keys is "-L leo send-keys -t <pane> -l .".
		if len(args) >= 7 && args[2] == "send-keys" && args[5] == "-l" && args[6] == inputProbe {
			probeSendKeys++
			if probeSendKeys < 3 {
				return exec.Command("false") // session not created yet
			}
			return exec.Command("true")
		}
		if len(args) >= 3 && args[2] == "capture-pane" {
			// Ready once the session exists; the pane also carries the body
			// so phase 3's confirm loop breaks immediately.
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"body\n")
		}
		return exec.Command("true")
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body", 10, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt should tolerate a late-appearing session: %v", err)
	}
	if probeSendKeys < 3 {
		t.Fatalf("expected the probe to retry past the missing-session window, got %d probe send-keys", probeSendKeys)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
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
			// Includes the body so phase 3's confirm loop breaks on its
			// first capture rather than exhausting the full budget.
			return exec.Command("printf", "%s", "some unrecognized pane\nno prompt glyph anywhere\nbody\n")
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

func TestProbeClassifier(t *testing.T) {
	codexReady := "  Tip: Our most capable model yet.\n› Use /skills to list available skills\n  gpt-5.6-sol default"
	codexProbe := "  Tip: Our most capable model yet.\n› .\n  gpt-5.6-sol default"
	opencodeReady := "┃\n┃  Ask anything... \"Fix a TODO in the codebase\"\n┃\n┃  Build · Qwen 3.6 35B A3B (local)"
	opencodeProbe := "┃\n┃  .\n┃\n┃  Build · Qwen 3.6 35B A3B (local)"
	tests := []struct {
		name, marker, pane string
		want               InputState
	}{
		{"codex placeholder is not probe", "› ", codexReady, InputEmpty},
		{"codex probe landed", "› ", codexProbe, InputHasContent},
		{"opencode placeholder is not probe", "┃", opencodeReady, InputEmpty},
		{"opencode probe landed", "┃", opencodeProbe, InputHasContent},
		{"no marker at all", "› ", "plain output\nno input box", InputUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProbeClassifier(tt.marker)(tt.pane); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInjectPromptTUICustomProfile proves InjectPromptTUI drives phase 1's
// readiness probe using the supplied Profile's Classify function rather than
// claude's classifyInput — mirroring the existing injectPrompt readiness
// tests' seam usage, but with a canned classifier standing in for a
// different harness's TUI.
func TestInjectPromptTUICustomProfile(t *testing.T) {
	var got [][]string
	classifyCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			// Carries the body so phase 3's confirm loop breaks on its
			// first capture instead of exhausting the full budget.
			return exec.Command("printf", "%s", "hello\n")
		}
		return exec.Command("true")
	}
	profile := Profile{
		Marker: "› ",
		Classify: func(pane string) InputState {
			classifyCalls++
			// First two probes report empty (not yet landed); third lands.
			if classifyCalls < 3 {
				return InputEmpty
			}
			return InputHasContent
		},
	}
	if err := InjectPromptTUI(context.Background(), "tmux", "leo-session-foo", "hello", profile); err != nil {
		t.Fatalf("InjectPromptTUI: %v", err)
	}
	if classifyCalls < 3 {
		t.Fatalf("expected the custom profile's Classify to be called >=3 times, got %d", classifyCalls)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
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

// enterCallIndex returns the index of the submit-Enter send-keys call in got,
// or -1 if none is present.
func enterCallIndex(got [][]string) int {
	for i, c := range got {
		if len(c) >= 4 && c[3] == "send-keys" && c[len(c)-1] == "Enter" {
			return i
		}
	}
	return -1
}

// captureCallsBefore counts capture-pane calls in got before index idx.
func captureCallsBefore(got [][]string, idx int) int {
	n := 0
	for _, c := range got[:idx] {
		if len(c) >= 4 && c[3] == "capture-pane" {
			n++
		}
	}
	return n
}

// TestInjectPromptConfirmsPasteBeforeSubmitting proves phase 3's post-paste
// confirm loop actually withholds Enter until the pasted body is visible in
// the pane — the fix for codex's async bracketed-paste commit dropping an
// Enter that arrives in the same burst as the paste.
func TestInjectPromptConfirmsPasteBeforeSubmitting(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 10
	submitConfirmPoll = time.Millisecond
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	const withheldCaptures = 4 // confirm-loop captures before the body shows
	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			if captureCalls == 1 {
				// Phase 1 readiness capture: claude is ready.
				return exec.Command("printf", "%s", paneWithInput(inputProbe))
			}
			// Phase 3 confirm-loop captures: body absent for a stretch, then present.
			if captureCalls <= 1+withheldCaptures {
				return exec.Command("printf", "%s", "no body here yet\n")
			}
			return exec.Command("printf", "%s", "codex-marker-body\nsecond line\n")
		}
		return exec.Command("true")
	}

	body := "codex-marker-body\nsecond line"
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", body, 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}

	idx := enterCallIndex(got)
	if idx == -1 {
		t.Fatalf("expected a submit Enter call, got none: %#v", got)
	}
	if idx != len(got)-1 {
		t.Fatalf("Enter must be the final call, got at index %d of %d: %#v", idx, len(got), got)
	}
	if n := captureCallsBefore(got, idx); n < 1+withheldCaptures {
		t.Fatalf("Enter fired too early: only %d capture-pane calls before it, want >= %d (withheld until body visible): %#v", n, 1+withheldCaptures, got)
	}
}

// TestInjectPromptConfirmAddsNoDelayWhenBodyLandsImmediately proves the
// claude/opencode path — where the pasted body appears in the pane on the
// very first confirm-loop capture — adds zero extra polling: the loop breaks
// on its first iteration and Enter fires immediately.
func TestInjectPromptConfirmAddsNoDelayWhenBodyLandsImmediately(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 25
	submitConfirmPoll = 200 * time.Millisecond // would be very slow if ever waited on
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			if captureCalls == 1 {
				return exec.Command("printf", "%s", paneWithInput(inputProbe))
			}
			// Every confirm-loop capture already shows the body: claude/opencode
			// commit a bracketed paste synchronously.
			return exec.Command("printf", "%s", "sync-body-marker\n")
		}
		return exec.Command("true")
	}

	body := "sync-body-marker"
	start := time.Now()
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", body, 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected no added latency on the synchronous-paste path, took %v", elapsed)
	}
	if captureCalls != 2 {
		t.Fatalf("expected exactly 2 capture-pane calls (1 readiness + 1 confirm), got %d: %#v", captureCalls, got)
	}
	idx := enterCallIndex(got)
	if idx != len(got)-1 {
		t.Fatalf("Enter must be the final call, got index %d of %d: %#v", idx, len(got), got)
	}
}

// TestInjectPromptConfirmFallsThroughToEnterOnBudgetExpiry proves the confirm
// loop never blocks or drops the message when the needle never appears
// (capture-pane unavailable, or a TUI whose rendering this heuristic can't
// see): it exhausts its bounded budget and still submits with Enter.
func TestInjectPromptConfirmFallsThroughToEnterOnBudgetExpiry(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 3
	submitConfirmPoll = time.Millisecond
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			if captureCalls == 1 {
				return exec.Command("printf", "%s", paneWithInput(inputProbe))
			}
			// Body never appears in the confirm loop.
			return exec.Command("printf", "%s", "body never lands here\n")
		}
		return exec.Command("true")
	}

	body := "unconfirmable-body"
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", body, 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	idx := enterCallIndex(got)
	if idx != len(got)-1 {
		t.Fatalf("Enter must still be sent as the final call even when unconfirmed, got index %d of %d: %#v", idx, len(got), got)
	}
	// 1 readiness capture + submitConfirmAttempts confirm-loop captures.
	if captureCalls != 1+submitConfirmAttempts {
		t.Fatalf("expected the confirm loop to exhaust its budget (%d captures after readiness), got %d total captures: %#v", submitConfirmAttempts, captureCalls, got)
	}
}

// TestInjectPromptConfirmEmptyBodyUsesFixedDelay proves an empty (or
// whitespace-only) body's confirm phase takes the short-needle fixed-delay
// fallback rather than skipping confirmation altogether: there's no
// distinctive line to derive a needle from, so submitConfirmNeedle returns
// "" (below submitNeedleMinRunes) and the loop falls back to a bounded fixed
// settle beat before Enter — never calling capture-pane in that fallback.
// (Prior to the short-needle floor, an empty needle skipped confirmation
// entirely; this test's expectation was updated accordingly — see the fix
// report.)
func TestInjectPromptConfirmEmptyBodyUsesFixedDelay(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 25
	submitConfirmPoll = 2 * time.Millisecond
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			return exec.Command("printf", "%s", paneWithInput(inputProbe))
		}
		return exec.Command("true")
	}

	start := time.Now()
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "   \n  ", 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	elapsed := time.Since(start)

	// Only the single phase-1 readiness capture — the fixed-delay fallback
	// never calls capture-pane.
	if captureCalls != 1 {
		t.Fatalf("expected no confirm-loop captures for an empty body, got %d total captures: %#v", captureCalls, got)
	}
	if want := time.Duration(submitConfirmFallbackDelays) * submitConfirmPoll; elapsed < want {
		t.Fatalf("expected the fixed-delay fallback to wait >= %v, took %v", want, elapsed)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("expected exactly 1 paste-buffer, got %d: %#v", n, got)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
	}
}

// TestInjectPromptShortBodyUsesFixedDelayNotNeedleMatch proves a body whose
// needle falls below submitNeedleMinRunes (e.g. "ok") does NOT rely on
// Contains-matching against the pane — a 2-rune needle could coincidentally
// already exist in the pane (a hint, placeholder, or prior output) before an
// async paste actually commits, which would break the loop early and fire
// Enter into an uncommitted paste. Instead it takes the bounded fixed delay:
// even though every capture-pane call here (including the very first, phase
// 1's readiness capture) already contains "ok", the confirm phase must still
// wait out the fixed delay rather than matching on the first opportunity.
func TestInjectPromptShortBodyUsesFixedDelayNotNeedleMatch(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 25
	submitConfirmPoll = 5 * time.Millisecond
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"ok\n")
		}
		return exec.Command("true")
	}

	start := time.Now()
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "ok", 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	elapsed := time.Since(start)

	if want := time.Duration(submitConfirmFallbackDelays) * submitConfirmPoll; elapsed < want {
		t.Fatalf("expected the fixed-delay fallback to wait >= %v, took %v (proves it did NOT break early on the coincidental \"ok\" match)", want, elapsed)
	}
	// Only the single phase-1 readiness capture — a Contains-matching loop
	// would have made at least one additional capture-pane call and broken
	// on it immediately (near-zero elapsed time) instead of waiting.
	if captureCalls != 1 {
		t.Fatalf("expected no confirm-loop capture-pane calls for a short body, got %d: %#v", captureCalls, got)
	}
	idx := enterCallIndex(got)
	if idx != len(got)-1 {
		t.Fatalf("Enter must be the final call, got index %d of %d: %#v", idx, len(got), got)
	}
}

// TestInjectPromptNeedleUsesFirstNonEmptyLine proves Fix 3: the confirm-loop
// needle is derived from the body's first NON-EMPTY line, not the literal
// first line — a body starting with blank lines (e.g. "\n\nreal text") still
// gets a distinctive needle and goes through the Contains-matching confirm
// loop, instead of silently falling back to the short/empty-needle path.
func TestInjectPromptNeedleUsesFirstNonEmptyLine(t *testing.T) {
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts = 10
	submitConfirmPoll = time.Millisecond
	defer func() { submitConfirmAttempts = origAttempts; submitConfirmPoll = origPoll }()

	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			if captureCalls == 1 {
				return exec.Command("printf", "%s", paneWithInput(inputProbe))
			}
			// Only matches once the pane shows the first NON-EMPTY line's
			// text — proving the needle skipped the leading blank lines
			// rather than deriving an empty needle from them.
			return exec.Command("printf", "%s", "hello world this is the real content\n")
		}
		return exec.Command("true")
	}

	body := "\n\nhello world this is the real content"
	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", body, 1, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	if captureCalls != 2 {
		t.Fatalf("expected exactly 2 captures (1 readiness + 1 confirm-loop match), got %d: %#v", captureCalls, got)
	}
	idx := enterCallIndex(got)
	if idx != len(got)-1 {
		t.Fatalf("Enter must be the final call, got index %d of %d: %#v", idx, len(got), got)
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

func TestClassifyInputDistinguishesMenusFromInputBox(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want inputState
	}{
		{"empty input box", paneWithInput(""), inputEmpty},
		{"probe char in box", paneWithInput("."), inputHasContent},
		{"real typed prompt", paneWithInput("hello world"), inputHasContent},
		{
			"numbered menu option after glyph",
			"  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n",
			inputUnknown,
		},
		{
			"confirm/cancel dialog chrome",
			"  Some dialog\n  ❯ Proceed\n  Enter to confirm · Esc to cancel\n",
			inputUnknown,
		},
		{
			"paren-style numbered option",
			"  ❯ 1) Option A\n    2) Option B\n",
			inputUnknown,
		},
		{"no glyph at all", "just some output\nno prompt here\n", inputUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyInput(c.pane); got != c.want {
				t.Fatalf("classifyInput = %v, want %v", got, c.want)
			}
		})
	}
}

// TestInjectPromptWaitsThroughMenu proves the injector does not paste while a
// startup-dialog menu is showing (classifyInput reports it as not-ready), and
// delivers exactly once after the dialog clears to a real input box — the
// scenario behind the dropped auto-wake message.
func TestInjectPromptWaitsThroughMenu(t *testing.T) {
	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			// First two captures show a blocking menu; then the real input
			// box, already carrying the body so phase 3's confirm loop
			// breaks on its first capture instead of exhausting the budget.
			if captureCalls < 3 {
				return exec.Command("printf", "%s", "  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n  Enter to confirm · Esc to cancel\n")
			}
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"body\n")
		}
		return exec.Command("true")
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body", 10, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}

	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	if captureCalls < 3 {
		t.Fatalf("expected to probe through the menu (>=3 captures), got %d", captureCalls)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
	}
}
