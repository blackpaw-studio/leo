package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

const (
	injectCommandTimeout = 5 * time.Second
	injectWaitDelay      = 100 * time.Millisecond
	// injectable so confirmation failure paths can be tested without waiting.
)

var injectConfirmAttempts = 15

var injectBufferNonce atomic.Uint64

// CommandFunc starts a tmux command. It makes strict injection usable by
// callers that own their command-execution seam.
type CommandFunc func(context.Context, string, ...string) *exec.Cmd

var (
	ErrComposerBusy    = errors.New("composer busy")
	ErrComposerUnknown = errors.New("composer unknown")
	ErrPasteFailed     = errors.New("paste failed")
)

// InjectInto pastes only into a visibly empty composer. It intentionally
// never probes with keys: interactive dispatch shares the composer with a
// human, so an uncertain state fails closed.
func InjectInto(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func() error) error {
	return InjectIntoWith(ctx, tmuxPath, paneID, classify, text, beforeEnter, execCommand)
}

// InjectIntoWith is InjectInto with an injectable tmux command runner.
func InjectIntoWith(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func() error, command CommandFunc) error {
	capture := func() (string, error) {
		cctx, cancel := context.WithTimeout(ctx, injectCommandTimeout)
		defer cancel()
		c := command(cctx, tmuxPath, Args("capture-pane", "-p", "-t", paneID)...)
		c.WaitDelay = injectWaitDelay
		out, err := c.Output()
		return string(out), err
	}
	before, err := capture()
	if err != nil {
		return fmt.Errorf("capture composer: %w", err)
	}
	switch classify(before) {
	case ComposerBusy:
		return ErrComposerBusy
	case ComposerEmpty:
	default:
		return ErrComposerUnknown
	}
	buffer := fmt.Sprintf("leo-dispatch-%s-%d", strings.TrimPrefix(paneID, "%"), injectBufferNonce.Add(1))
	if err := runInjectCommand(ctx, tmuxPath, command, Args("set-buffer", "-b", buffer, "--", text)...); err != nil {
		return fmt.Errorf("set paste buffer: %w", err)
	}
	bufferDeleted := false
	defer func() {
		if !bufferDeleted {
			// paste-buffer -d normally deletes this, but a failed paste leaves it
			// behind. Cleanup is bounded by runInjectCommand and best-effort.
			_ = runInjectCommand(ctx, tmuxPath, command, Args("delete-buffer", "-b", buffer)...)
		}
	}()
	if err := runInjectCommand(ctx, tmuxPath, command, Args("paste-buffer", "-b", buffer, "-d", "-p", "-t", paneID)...); err != nil {
		return fmt.Errorf("paste buffer: %w", err)
	}
	bufferDeleted = true
	// The needle is tail-anchored (composerConfirmNeedle: submitConfirmNeedle
	// applied to the body AFTER the same per-line stripLeadingComposerGlyph
	// normalization composerScopeText applies to the rendered composer — see
	// composerConfirmNeedle for why the two sides must match) and matched
	// Contains-style against the composer's JOINED, whitespace-normalized
	// scope text (composerPasteConfirmed below) — not HasPrefix per rendered
	// row. A long single-line body wraps across composer rows; a
	// row-boundary-sensitive match would put the tail mid-row and never match
	// no matter how long the loop waited (the interactive-dispatch "paste
	// failed" / Enter-lands-inside-the-paste bug). Only a genuinely empty
	// (whitespace-only) body skips confirmation entirely.
	needle := composerConfirmNeedle(text)
	if needle == "" {
		return ErrPasteFailed
	}
	// Below submitNeedleMinRunes, count-based baseline matching isn't safe:
	// the composer's own empty-state hint text ("Ask Codex to do anything",
	// claude's equivalent) can coincidentally CONTAIN a 1-2 rune needle
	// somewhere mid-word ("a" in "anything"), inflating the baseline count —
	// the hint then disappears once real text is typed, so the count never
	// exceeds that contaminated baseline and confirmation never succeeds.
	// Fall back to the pre-baseline, per-row PRESENCE check instead (see
	// composerRowStartsWithNeedle): a short needle essentially never STARTS
	// an entire composer row by coincidence, so no baseline is needed at all
	// — this mirrors InjectInto's behavior before the count-based baseline
	// logic existed.
	useCountMatch := len([]rune(needle)) >= submitNeedleMinRunes
	baselineNeedle, baselinePlaceholder := 0, 0
	if useCountMatch {
		// Baseline: how many times the needle and the collapsed-paste
		// placeholder ("[Pasted text ...]"/"[Pasted Content ...]") already
		// appear in the composer's scope BEFORE anything is pasted (reusing
		// the classify capture above — no extra round trip). Presence alone
		// isn't proof the new paste landed: a resend of an identical
		// message, a harness echo of a prior prompt, or a placeholder left
		// over from an earlier paste that was never cleared can already
		// satisfy a presence-only check. The confirm loop requires each
		// occurrence count to exceed its own baseline, not merely be
		// present.
		if scope, ok := composerScopeText(before); ok {
			baselineNeedle = strings.Count(stripWhitespace(scope), needle)
			baselinePlaceholder = composerPlaceholderCount(scope)
		}
	}
	confirmed := false
	matchedOnce := false
	var lastNormalized string
	for i := 0; i < injectConfirmAttempts; i++ {
		after, err := capture()
		if err == nil && after != before {
			if useCountMatch {
				isMatch, normalized := composerPasteConfirmed(after, needle, baselineNeedle, baselinePlaceholder)
				if isMatch {
					if matchedOnce && normalized == lastNormalized {
						confirmed = true
						break
					}
					matchedOnce = true
					lastNormalized = normalized
				} else {
					matchedOnce = false
				}
			} else if composerRowStartsWithNeedle(after, needle) {
				confirmed = true
				break
			}
		}
		if i+1 < injectConfirmAttempts {
			wait := time.NewTimer(submitConfirmPoll)
			select {
			case <-wait.C:
			case <-ctx.Done():
				wait.Stop()
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if !confirmed {
		return ErrPasteFailed
	}
	if beforeEnter != nil {
		if err := beforeEnter(); err != nil {
			return err
		}
	}
	if err := runInjectCommand(ctx, tmuxPath, command, Args("send-keys", "-t", paneID, "Enter")...); err != nil {
		return fmt.Errorf("submit paste: %w", err)
	}
	return nil
}

// composerGlyphs are the prompt/border glyphs that may lead a composer row:
// claude's "❯", codex's "›", and opencode's bordered-panel "┃".
var composerGlyphs = []string{"❯", "›", "┃"}

// stripLeadingComposerGlyph strips AT MOST ONE leading composer/border glyph
// from line — but only when it is unambiguously chrome, not body content
// that merely starts with the same character. A real prompt/border glyph
// always renders as "glyph + space" (e.g. "❯ text", "┃ text") or bare alone
// on its row (an empty composer, or a bordered panel's blank border row);
// pasted body text that happens to start a wrapped row with a literal ›, ❯,
// or ┃ character is never followed immediately by a space in that position
// (it's mid-word/mid-punctuation), so it is left untouched. Stripping
// unconditionally — the previous behavior — silently ate body content: a
// paste whose tail contained "›" (a markdown blockquote marker, a shell
// prompt being quoted, etc.) landing at the start of a wrapped row would
// have that character removed, corrupting the text the confirm loop matches
// against and breaking confirmation for otherwise-landed pastes.
func stripLeadingComposerGlyph(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	for {
		stripped := false
		for _, glyph := range composerGlyphs {
			rest, ok := strings.CutPrefix(trimmed, glyph)
			if !ok {
				continue
			}
			if rest == "" || strings.HasPrefix(rest, " ") {
				trimmed = strings.TrimPrefix(rest, " ")
				stripped = true
				break
			}
		}
		if !stripped {
			return trimmed
		}
	}
}

// composerConfirmNeedle derives InjectInto's confirm-loop needle: the same
// tail-of-whitespace-normalized-body derivation as submitConfirmNeedle
// (inject.go), but the body is first run through stripLeadingComposerGlyph
// PER LINE — the exact same normalization composerScopeText applies to each
// rendered composer row. Without this, a body line that itself starts with
// "› " (e.g. quoting a codex prompt, "hello\n› world") has that leading "› "
// stripped from the RENDERED composer scope (it's indistinguishable from
// real chrome), but not from a needle derived from the raw, unstripped body
// — leaving the needle carrying a "›" character the scope no longer has, so
// the two sides could never match again. inject.go's own needle derivation
// is untouched: its confirm loop matches against the raw, unscoped capture,
// which never goes through this per-row glyph stripping in the first place.
func composerConfirmNeedle(text string) string {
	lines := strings.Split(text, "\n")
	cleaned := make([]string, len(lines))
	for i, line := range lines {
		cleaned[i] = stripLeadingComposerGlyph(line)
	}
	return submitConfirmNeedle(strings.Join(cleaned, "\n"))
}

// composerRowStartsWithNeedle reports whether capture's composer scope shows
// a collapsed-paste placeholder, or any of its rows (trimmed) STARTS WITH
// needle. This is InjectInto's confirmation behavior from before the
// count-based baseline logic existed (see composerPasteConfirmed) — used
// only for needles too short to trust with Contains/count matching (below
// submitNeedleMinRunes), where a baseline would itself be unreliable: the
// composer's own empty-state hint text can coincidentally CONTAIN a 1-2 rune
// needle somewhere mid-word without ever actually STARTING a row with it, so
// a per-row prefix check stays reliable with no baseline at all.
func composerRowStartsWithNeedle(capture, needle string) bool {
	scope, ok := composerScopeText(capture)
	if !ok {
		return false
	}
	if composerPlaceholderCount(scope) > 0 {
		return true
	}
	for _, line := range strings.Split(scope, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), needle) {
			return true
		}
	}
	return false
}

// composerScopeText returns the identified composer scope's lines in
// capture — the ruled composer box for claude, the marker-prefixed block for
// opencode, or the "›"-prefixed tail for codex — with each row's leading
// composer/border glyph stripped (see stripLeadingComposerGlyph), then
// joined into one string. When more than one composer-shaped box exists in
// the capture (e.g. a stale/historical claude-style block above the live
// one in scrollback), the BOTTOM-most is used: claudeComposerBox and the
// codex "›" fallback both scan from the end of the capture backward and
// return on their first (i.e. lowest) match, so the live composer — always
// the last thing rendered — wins over anything stale further up.
//
// Scoping matching to this text (rather than the raw capture) is what keeps
// an earlier copy of the message sitting in scrollback history from causing
// a false confirm; joining the CLEANED lines (rather than checking each row
// in isolation) is what lets a match survive the composer wrapping a long
// line across several rendered rows — opencode in particular re-renders its
// left border glyph on every wrapped row, and an unstripped border
// character sitting mid-string would otherwise split a needle that happens
// to wrap right at that column. ok=false means no composer scope could be
// identified (history-only capture, or a mid-transition frame) — the caller
// treats that as "cannot confirm yet," not "definitely not landed."
func composerScopeText(capture string) (string, bool) {
	lines := strings.Split(capture, "\n")
	var scope []string
	if _, composerLine, bottom, ok := claudeComposerBox(lines); ok {
		scope = lines[composerLine:bottom]
	} else if composer, ok := openCodeComposerLines(lines); ok {
		scope = composer
	} else {
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "›") {
				scope = lines[i:]
				break
			}
		}
	}
	if scope == nil {
		return "", false
	}
	cleaned := make([]string, len(scope))
	for i, line := range scope {
		cleaned[i] = stripLeadingComposerGlyph(line)
	}
	return strings.Join(cleaned, "\n"), true
}

// composerPlaceholderCount counts how many times a collapsed-paste
// placeholder ("[Pasted text ...]" or "[Pasted Content ...]") appears in
// scope — some TUIs replace a large paste with this marker instead of
// rendering it verbatim.
func composerPlaceholderCount(scope string) int {
	return strings.Count(scope, "[Pasted text") + strings.Count(scope, "[Pasted Content")
}

// composerPasteConfirmed reports whether capture's composer scope proves the
// NEW paste landed, given needle (already whitespace-normalized) and the two
// pre-paste baselines: baselineNeedle (the needle's occurrence count in the
// composer before anything was pasted) and baselinePlaceholder (likewise for
// the collapsed-paste placeholder). It returns the scope's normalized text
// alongside the verdict so the caller can require one further stable
// (unchanged) poll before trusting a single match — mirroring inject.go's
// confirm loop.
//
// Two independent signals confirm a landed paste, and BOTH are count-based
// against their own baseline — never mere presence, which a resend of an
// identical message, a harness echo, or a placeholder left over from an
// earlier paste that was never cleared could already satisfy before the new
// paste even lands:
//   - the placeholder's occurrence count in the composer exceeds
//     baselinePlaceholder;
//   - the needle's occurrence count in the composer exceeds baselineNeedle.
//
// Matching is Contains-style against the JOINED, whitespace-stripped scope
// text, not HasPrefix per rendered row: a long single-line body can wrap
// across several composer rows, landing the needle mid-row, where a
// per-row-prefix check would never match regardless of how long the loop
// waited (the interactive-dispatch "paste failed" bug this replaces).
func composerPasteConfirmed(capture, needle string, baselineNeedle, baselinePlaceholder int) (matched bool, normalized string) {
	scope, ok := composerScopeText(capture)
	if !ok {
		return false, ""
	}
	normalized = stripWhitespace(scope)
	if composerPlaceholderCount(scope) > baselinePlaceholder {
		return true, normalized
	}
	if needle == "" {
		return false, normalized
	}
	count := strings.Count(normalized, needle)
	return count > baselineNeedle, normalized
}

func runInjectCommand(ctx context.Context, tmuxPath string, command CommandFunc, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, injectCommandTimeout)
	defer cancel()
	cmd := command(cctx, tmuxPath, args...)
	cmd.WaitDelay = injectWaitDelay
	return cmd.Run()
}
