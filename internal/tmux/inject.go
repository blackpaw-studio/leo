package tmux

import (
	"context"
	"fmt"
	"os/exec"
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

// inputProbe is a single throwaway character typed to test whether claude is
// accepting keystrokes yet. One character is fully removable with a single
// Ctrl-U (which only kills one input line), so probing never risks leaving
// residue ahead of the real prompt.
const inputProbe = "."

type inputState int

const (
	inputUnknown    inputState = iota // pane couldn't be read/parsed — don't block on it
	inputEmpty                        // input box present but empty (paste not landed)
	inputHasContent                   // input box carries text (paste landed)
)

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
	return injectPrompt(ctx, tmuxPath, session, body, injectReadyAttempts, injectReadyPoll)
}

// injectPrompt is the testable inner form with injectable probe bounds.
func injectPrompt(ctx context.Context, tmuxPath, session, body string, maxAttempts int, poll time.Duration) error {
	runKey := func(keys ...string) error {
		args := append([]string{"send-keys", "-t", session}, keys...)
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
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		st := paneInputState(ctx, tmuxPath, session)
		if err := runKey("C-u"); err != nil {
			return err
		}
		switch st {
		case inputHasContent:
			ready = true
		case inputEmpty:
			// Box is drawn but the probe was dropped — claude still booting.
			sawInputBox = true
		case inputUnknown:
			// No recognizable input box yet (mid-boot before the TUI draws).
		}
		if ready {
			break
		}
	}
	if !ready && sawInputBox {
		// The input box exists but never accepted our probe within the budget —
		// pasting the body would be dropped too. Fail fast instead of hanging.
		return fmt.Errorf("claude session %q never started accepting input after %d attempts", session, maxAttempts)
	}
	// ready, or the pane format was never recognized (fall open rather than
	// block, so this can't make an otherwise-working session worse).

	// Phase 2: stage and paste the body exactly once, then submit.
	buf := sessionBufferName(session)
	for _, args := range [][]string{
		Args("set-buffer", "-b", buf, "--", body),
		Args("paste-buffer", "-b", buf, "-t", session, "-d"),
		Args("send-keys", "-t", session, "Enter"),
	} {
		cmd := execCommand(ctx, tmuxPath, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", args[2], err, string(out))
		}
	}
	return nil
}

// InputHasContent reports whether a captured claude pane shows text waiting in
// its input box (the prompt-glyph line carries non-whitespace). Callers use it
// to confirm typed text landed before submitting with Enter — an Enter that
// arrives in the same input burst as the text is treated as a literal newline
// by claude's Ink REPL, not a submit, leaving the message unsent.
func InputHasContent(pane string) bool {
	return classifyInput(pane) == inputHasContent
}

// paneInputState captures the session's visible pane and classifies its input
// box. Read failures classify as inputUnknown so the caller falls open.
func paneInputState(ctx context.Context, tmuxPath, session string) inputState {
	out, err := execCommand(ctx, tmuxPath, Args("capture-pane", "-p", "-t", session)...).Output()
	if err != nil {
		return inputUnknown
	}
	return classifyInput(string(out))
}

// classifyInput inspects a captured pane for claude's input line (the last line
// beginning with the prompt glyph) and reports whether it carries text.
func classifyInput(pane string) inputState {
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(line, claudePromptGlyph) {
			continue
		}
		if strings.TrimSpace(line[len(claudePromptGlyph):]) == "" {
			return inputEmpty
		}
		return inputHasContent
	}
	return inputUnknown
}

// AbortPrompt cancels a mid-turn claude by sending Escape then Ctrl-C.
// Best-effort; records the first error but continues with both keys.
func AbortPrompt(ctx context.Context, tmuxPath, session string) error {
	keys := []string{"Escape", "C-c"}
	var firstErr error
	for _, k := range keys {
		cmd := execCommand(ctx, tmuxPath, Args("send-keys", "-t", session, k)...)
		if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("tmux send-keys %s: %w: %s", k, err, string(out))
		}
	}
	return firstErr
}
