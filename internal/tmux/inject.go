package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// claudePromptGlyph marks the start of claude's interactive input line. We use
// it to tell an empty input box (paste dropped) from one carrying our prompt
// (paste landed) before committing the turn with Enter.
const claudePromptGlyph = "❯ "

// injectReadyAttempts / injectReadyPoll bound the cold-start readiness probe:
// ~injectReadyAttempts*injectReadyPoll (≈60s) of total wait for a freshly
// booted claude — which can spend tens of seconds loading plugins/MCP servers
// before its input box accepts input — to start listening.
const (
	injectReadyAttempts = 120
	injectReadyPoll     = 500 * time.Millisecond
)

// submitConfirm* bound the post-paste "did the body land before we hit
// Enter?" poll. Some TUIs (codex) commit a bracketed paste asynchronously,
// so an Enter sent in the same burst as the paste is dropped. We poll until
// a distinctive slice of the body appears in the pane, then submit.
//
// Package-level vars (not consts) so tests can shrink them to keep the
// confirm-loop tests fast.
var (
	submitConfirmAttempts = 25
	submitConfirmPoll     = 200 * time.Millisecond
)

// submitConfirmNeedleRunes bounds how much of body's first line we look for
// in the pane before submitting with Enter — long enough to be distinctive,
// short enough to survive an input-line wrapping the pasted text.
const submitConfirmNeedleRunes = 24

// inputProbe is a single throwaway character typed to test whether claude is
// accepting keystrokes yet. One character is fully removable with a single
// Ctrl-U (which only kills one input line), so probing never risks leaving
// residue ahead of the real prompt.
const inputProbe = "."

// InputState classifies a captured pane's input box during the readiness
// probe.
type InputState int

const (
	InputUnknown    InputState = iota // pane couldn't be read/parsed — don't block on it
	InputEmpty                        // input box present but probe not landed
	InputHasContent                   // input box carries the probe/typed text
)

// inputState/inputUnknown/inputEmpty/inputHasContent are unexported aliases
// for InputState and its values, kept so pre-existing internal call sites
// and tests using the lowercase names keep compiling unchanged.
type inputState = InputState

const (
	inputUnknown    = InputUnknown
	inputEmpty      = InputEmpty
	inputHasContent = InputHasContent
)

// Profile describes one harness TUI's input-line shape for the readiness
// probe. Marker prefixes the input line. Classify inspects a captured pane
// and reports the input state; harnesses whose input line renders
// placeholder hints (codex, opencode) must use ProbeClassifier, which only
// accepts the exact probe char as "content" — a bare non-empty check would
// mistake the placeholder for a landed probe.
type Profile struct {
	Marker   string
	Classify func(pane string) InputState
}

// ClaudeProfile is claude's probe profile: the existing classifier with its
// menu-option and dialog-chrome guards, unchanged.
func ClaudeProfile() Profile {
	return Profile{Marker: claudePromptGlyph, Classify: classifyInput}
}

// ProbeClassifier returns a classifier for TUIs whose input line starts with
// marker and may render placeholder hints: only a line whose content is
// exactly the probe char counts as landed; any other content (including
// placeholders, or text a human left in an attached box) reports InputEmpty
// so the probe keeps waiting.
//
// Some TUIs (opencode) render a bordered multi-line panel where every row
// shares the same leading marker (e.g. "┃") — the actual input row is not
// necessarily the last marker-prefixed line (a footer status line can also
// carry the marker). So this scans every marker-prefixed line for an exact
// probe match rather than trusting only the last one; if none carries the
// exact probe but at least one marker line was seen, the box is present but
// not yet landed (InputEmpty).
func ProbeClassifier(marker string) func(string) InputState {
	return func(pane string) InputState {
		lines := strings.Split(pane, "\n")
		sawMarker := false
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimLeft(lines[i], " \t")
			if !strings.HasPrefix(line, marker) {
				continue
			}
			sawMarker = true
			content := strings.TrimSpace(line[len(marker):])
			if content == inputProbe {
				return InputHasContent
			}
		}
		if sawMarker {
			return InputEmpty
		}
		return InputUnknown
	}
}

// execCommand is the seam tests replace.
var execCommand = exec.CommandContext

// sessionBufferName derives a tmux buffer name unique to the target session
// so concurrent injection calls into different sessions never share the same
// staging buffer. Two pump goroutines (one per session) can therefore run
// set-buffer / paste-buffer in any interleaving without clobbering each
// other.
func sessionBufferName(session string) string {
	return "leo-" + session
}

// InjectPrompt sends body to the claude running in `session` as a single
// submission. Uses set-buffer + paste-buffer (-d deletes after paste) to
// avoid character-by-character races; multi-line bodies preserved; Enter
// submits.
//
// A freshly booted claude (still loading plugins/MCP servers and wiring up its
// input handler) renders an input box but silently drops input for a while;
// pasting into that window loses the prompt and the task hangs until its
// timeout. So InjectPrompt first probes readiness — typing a single throwaway
// character and confirming claude echoes it — then clears the probe and pastes
// the real body exactly once. The body is never pasted more than once, so it
// can never be stacked/duplicated regardless of timing.
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error {
	return InjectPromptTUI(ctx, tmuxPath, session, body, ClaudeProfile())
}

// InjectPromptTUI is the harness-neutral entry point: it sends body to the
// TUI running in `session` as a single submission, probing readiness with p's
// classifier before pasting. InjectPrompt is a thin wrapper over this using
// ClaudeProfile.
func InjectPromptTUI(ctx context.Context, tmuxPath, session, body string, p Profile) error {
	return injectPromptProfile(ctx, tmuxPath, session, body, p, injectReadyAttempts, injectReadyPoll)
}

// injectPrompt is the testable inner form with injectable probe bounds, kept
// with its pre-existing signature (defaulting to ClaudeProfile) so existing
// call sites and tests exercising the claude path are unaffected by the
// profile generalization.
func injectPrompt(ctx context.Context, tmuxPath, session, body string, maxAttempts int, poll time.Duration) error {
	return injectPromptProfile(ctx, tmuxPath, session, body, ClaudeProfile(), maxAttempts, poll)
}

// injectPromptProfile is the profile-parameterized inner form.
func injectPromptProfile(ctx context.Context, tmuxPath, session, body string, p Profile, maxAttempts int, poll time.Duration) error {
	runKey := func(keys ...string) error {
		args := append([]string{"send-keys", "-t", PaneTarget(session)}, keys...)
		cmd := execCommand(ctx, tmuxPath, Args(args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux send-keys %v: %w: %s", keys, err, string(out))
		}
		return nil
	}

	// Phase 1: wait until claude is actually accepting input. Type one probe
	// char, see whether it lands in the input box, then clear it. A single char
	// is fully cleared by one Ctrl-U, so the probe never leaves residue.
	ready := false
	sawInputBox := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := runKey("-l", inputProbe); err != nil {
			// The session may not exist yet — a just-resumed (idle-suspended)
			// agent's tmux new-session lags the spawn call, which registers
			// state and starts the supervise goroutine asynchronously — or this
			// is a transient tmux error. Treat it as "not ready", wait, and
			// retry within the readiness budget rather than aborting. A session
			// that never appears falls through to Phase 2 below, which surfaces
			// a real error.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		st := paneInputState(ctx, tmuxPath, session, p)
		if err := runKey("C-u"); err != nil {
			return err
		}
		switch st {
		case InputHasContent:
			ready = true
		case InputEmpty:
			// Box is drawn but the probe was dropped — TUI still booting.
			sawInputBox = true
		case InputUnknown:
			// No recognizable input box yet (mid-boot before the TUI draws).
		}
		if ready {
			break
		}
	}
	if !ready && sawInputBox {
		// The input box exists but never accepted our probe within the budget —
		// pasting the body would be dropped too. Fail fast instead of hanging.
		return fmt.Errorf("session %q's TUI never started accepting input after %d attempts", session, maxAttempts)
	}
	// ready, or the pane format was never recognized (fall open rather than
	// block, so this can't make an otherwise-working session worse).

	// Phase 2: stage and paste the body exactly once.
	buf := sessionBufferName(session)
	for _, args := range [][]string{
		Args("set-buffer", "-b", buf, "--", body),
		Args("paste-buffer", "-b", buf, "-t", PaneTarget(session), "-d"),
	} {
		cmd := execCommand(ctx, tmuxPath, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", args[2], err, string(out))
		}
	}

	// Phase 3: confirm the pasted body actually landed in the pane before
	// submitting. Some TUIs (codex) commit a bracketed paste asynchronously,
	// so an Enter fired immediately after paste-buffer can arrive before the
	// text lands and gets silently dropped. claude/opencode render the body
	// synchronously, so this loop breaks on its first iteration for them —
	// zero added latency. This is best-effort: a capture-pane error, or the
	// needle never appearing within the budget, still falls through to
	// sending Enter (never blocks/loses the message).
	if needle := submitConfirmNeedle(body); needle != "" {
		for attempt := 0; attempt < submitConfirmAttempts; attempt++ {
			out, err := execCommand(ctx, tmuxPath, Args("capture-pane", "-p", "-t", PaneTarget(session))...).Output()
			if err == nil && strings.Contains(string(out), needle) {
				break
			}
			if attempt == submitConfirmAttempts-1 {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(submitConfirmPoll):
			}
		}
	}

	if err := runKey("Enter"); err != nil {
		return err
	}
	return nil
}

// submitConfirmNeedle derives a short, distinctive slice of body's first line
// to look for in the pane before submitting — long enough to be unlikely to
// appear by coincidence, short enough to survive the input line wrapping the
// pasted text. Returns "" for an empty/whitespace-only body, which callers
// treat as "nothing to confirm — just submit."
func submitConfirmNeedle(body string) string {
	firstLine := body
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		firstLine = body[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return ""
	}
	runes := []rune(firstLine)
	if len(runes) > submitConfirmNeedleRunes {
		runes = runes[:submitConfirmNeedleRunes]
	}
	return string(runes)
}

// PaneInputHasContent reports whether a captured claude pane shows text
// waiting in its input box (the prompt-glyph line carries non-whitespace).
// Callers use it to confirm typed text landed before submitting with Enter —
// an Enter that arrives in the same input burst as the text is treated as a
// literal newline by claude's Ink REPL, not a submit, leaving the message
// unsent.
//
// Named PaneInputHasContent (rather than InputHasContent) to avoid colliding
// with the InputHasContent InputState value.
func PaneInputHasContent(pane string) bool {
	return classifyInput(pane) == InputHasContent
}

// paneInputState captures the session's visible pane and classifies its input
// box using p's Classify. Read failures classify as InputUnknown so the
// caller falls open.
func paneInputState(ctx context.Context, tmuxPath, session string, p Profile) InputState {
	out, err := execCommand(ctx, tmuxPath, Args("capture-pane", "-p", "-t", PaneTarget(session))...).Output()
	if err != nil {
		return InputUnknown
	}
	return p.Classify(string(out))
}

// menuOptionPattern matches a numbered selection-menu option like "1. Yes" or
// "2) No" — the shape of claude's interactive dialog options. Such a line is not
// a content-bearing input box even though it follows the prompt glyph.
var menuOptionPattern = regexp.MustCompile(`^\d+[.)]\s`)

// hasDialogChrome reports whether a captured pane shows an interactive dialog's
// confirm/cancel footer rather than a plain input box.
func hasDialogChrome(pane string) bool {
	return strings.Contains(pane, "Enter to confirm") && strings.Contains(pane, "Esc to cancel")
}

// classifyInput inspects a captured pane for claude's input line (the last line
// beginning with the prompt glyph) and reports whether it carries text. A
// selection menu or a confirm/cancel dialog is reported as inputUnknown — the
// glyph there is a menu selector, not a ready input box, so callers keep waiting
// instead of pasting into the dialog.
func classifyInput(pane string) InputState {
	if hasDialogChrome(pane) {
		return InputUnknown
	}
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(line, claudePromptGlyph) {
			continue
		}
		content := strings.TrimSpace(line[len(claudePromptGlyph):])
		if content == "" {
			return InputEmpty
		}
		if menuOptionPattern.MatchString(content) {
			return InputUnknown
		}
		return InputHasContent
	}
	return InputUnknown
}

// AbortPrompt cancels a mid-turn claude by sending Escape then Ctrl-C.
// Best-effort; records the first error but continues with both keys.
func AbortPrompt(ctx context.Context, tmuxPath, session string) error {
	keys := []string{"Escape", "C-c"}
	var firstErr error
	for _, k := range keys {
		cmd := execCommand(ctx, tmuxPath, Args("send-keys", "-t", PaneTarget(session), k)...)
		if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("tmux send-keys %s: %w: %s", k, err, string(out))
		}
	}
	return firstErr
}
