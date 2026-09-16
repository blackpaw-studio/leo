package tmux

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const claudeEmptyComposer = "─\n❯\n─"

func TestInjectIntoRejectsWithoutKeys(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("echo", "busy")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerBusy }, "hello", nil); !errors.Is(err, ErrComposerBusy) {
		t.Fatalf("error = %v", err)
	}
	if len(calls) != 1 || !slices.Contains(calls[0], "capture-pane") {
		t.Fatalf("writes = %#v", calls)
	}
	calls = nil
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerUnknown }, "hello", nil); !errors.Is(err, ErrComposerUnknown) {
		t.Fatalf("error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("writes = %#v", calls)
	}
}

func TestInjectIntoPasteConfirmArmOrder(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	capture := 0
	armed := false
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			capture++
			if capture == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "─\n❯ hello\n─")
		}
		if slices.Contains(args, "send-keys") && !armed {
			t.Fatal("Enter sent before arm")
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", func() error { armed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

func TestInjectIntoUsesNamedBufferAndConfirmsMultilineCollapsedPaste(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "─\n❯ [Pasted text #1 +4 lines]\n─")
		}
		return exec.Command("true")
	}
	text := "first line is distinctive enough for confirmation\nsecond\nthird\nfourth\nfifth"
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, text, nil); err != nil {
		t.Fatal(err)
	}
	var buffer string
	for _, call := range calls {
		if slices.Contains(call, "set-buffer") {
			i := slices.Index(call, "set-buffer")
			if len(call) < i+4 || call[i+1] != "-b" {
				t.Fatalf("set-buffer call = %#v", call)
			}
			buffer = call[i+2]
		}
		if slices.Contains(call, "paste-buffer") {
			i := slices.Index(call, "paste-buffer")
			want := []string{"paste-buffer", "-b", buffer, "-d", "-p", "-t", "%1"}
			if !slices.Equal(call[i:], want) {
				t.Fatalf("paste-buffer call = %#v, buffer = %q", call, buffer)
			}
		}
	}
	if buffer == "" {
		t.Fatal("no named buffer")
	}
}

func TestComposerPasteConfirmedRecognizesCodexPastedContent(t *testing.T) {
	if matched, _ := composerPasteConfirmed("› [Pasted Content 2611 chars]", stripWhitespace("first line"), 0, 0); !matched {
		t.Fatal("Codex pasted-content placeholder was not confirmed")
	}
	if matched, _ := composerPasteConfirmed("›", stripWhitespace("first line"), 0, 0); matched {
		t.Fatal("empty Codex composer was confirmed")
	}
}

// TestComposerScopeTextPreservesEmbeddedGlyphNotFollowedBySpace proves
// stripLeadingComposerGlyph only strips a real prompt/border glyph (always
// "glyph + space" or bare alone on its row), not body content that merely
// starts a row with the same character. A body ending in "›no-space..." (a
// literal "›" with no trailing space — a markdown blockquote marker, a shell
// prompt being quoted, etc.) landing as an entire composer row must still
// confirm: the old unconditional-strip behavior ate the "›" and broke the
// Contains match against the needle it derived from.
func TestComposerScopeTextPreservesEmbeddedGlyphNotFollowedBySpace(t *testing.T) {
	body := "›no-space-tail-XYZ98"
	needle := submitConfirmNeedle(body)
	if !strings.HasPrefix(needle, "›") {
		t.Fatalf("test setup invalid: needle %q does not start with the embedded glyph", needle)
	}
	capture := needle // the composer's sole row, recognized via the codex "›" fallback.

	matched, _ := composerPasteConfirmed(capture, needle, 0, 0)
	if !matched {
		t.Fatalf("composerPasteConfirmed stripped a body-content '›' that wasn't a real prompt glyph (needle %q)", needle)
	}
}

// TestComposerScopeTextStripsRealLeadingGlyph proves the fix doesn't go too
// far the other way: an actual prompt glyph ("❯ " / "› " / bare "┃") is
// still stripped, since it's not part of the pasted body.
func TestComposerScopeTextStripsRealLeadingGlyph(t *testing.T) {
	scope, ok := composerScopeText("─\n❯ hello world\n─")
	if !ok {
		t.Fatal("composerScopeText: no scope found")
	}
	if strings.Contains(scope, "❯") {
		t.Fatalf("scope = %q, real leading glyph was not stripped", scope)
	}
}

// TestInjectIntoRequiresPlaceholderCountToExceedBaseline proves the
// collapsed-paste placeholder ("[Pasted text ...]"/"[Pasted Content ...]")
// is matched count-based against its own baseline, exactly like the needle:
// a placeholder already visible in the composer before anything is pasted
// (left over from an earlier paste that was never cleared, or a stale
// Claude-shaped box sitting above a codex composer) must NOT satisfy
// confirmation on its own — only an INCREASE in placeholder count proves the
// new paste landed.
func TestInjectIntoRequiresPlaceholderCountToExceedBaseline(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := injectConfirmAttempts
	injectConfirmAttempts = 6
	defer func() { injectConfirmAttempts = origAttempts }()

	stale := "─\n❯ [Pasted text #1 +4 lines]\n─"
	// Differs from `stale` byte-for-byte (an extra trailing blank line) so
	// the loop's after!=before guard doesn't short-circuit the test before
	// ever reaching composerPasteConfirmed — but the placeholder occurrence
	// count is still exactly 1, same as the baseline: no new placeholder
	// ever visibly appears.
	staleWithNoise := stale + "\n"
	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", stale)
			}
			return exec.Command("echo", staleWithNoise)
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v, want paste confirmation failure (a stale placeholder must not satisfy the confirm loop)", err)
	}
	for _, c := range calls {
		if slices.Contains(c, "send-keys") && c[len(c)-1] == "Enter" {
			t.Fatalf("unexpected Enter: %#v", calls)
		}
	}
}

// TestInjectIntoConfirmsOnNewPlaceholderBeyondBaseline proves the mirror
// case: when a genuinely NEW placeholder appears (count exceeds baseline),
// confirmation succeeds and Enter is sent.
func TestInjectIntoConfirmsOnNewPlaceholderBeyondBaseline(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	empty := "─\n❯ \n─"
	landed := "─\n❯ [Pasted text #1 +4 lines]\n─"
	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", empty)
			}
			return exec.Command("echo", landed)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", nil); err != nil {
		t.Fatalf("InjectInto: %v", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

// TestComposerScopeTextUsesBottomMostBox proves that when a capture contains
// more than one composer-shaped box (a stale/historical one, e.g. left over
// in scrollback from an earlier turn, above the live one), matching uses the
// BOTTOM-most box — the live composer, always the last thing rendered — not
// the stale one, even though the stale box's content would otherwise satisfy
// the needle.
func TestComposerScopeTextUsesBottomMostBox(t *testing.T) {
	capture := "─\n❯ stale-old-content-should-be-ignored\n─\nsome transcript output in between\n─\n❯ live-current-content\n─"
	scope, ok := composerScopeText(capture)
	if !ok {
		t.Fatal("composerScopeText: no scope found")
	}
	if strings.Contains(scope, "stale") {
		t.Fatalf("scope = %q, used the stale (top) box instead of the bottom-most one", scope)
	}
	if !strings.Contains(scope, "live-current-content") {
		t.Fatalf("scope = %q, did not use the bottom-most (live) box", scope)
	}
}

func TestInjectIntoArmErrorPreventsEnter(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "─\n❯ hello\n─")
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", func() error { return errors.New("settled") })
	if err == nil {
		t.Fatal("arm error accepted")
	}
	for _, call := range calls {
		if slices.Contains(call, "Enter") {
			t.Fatalf("unexpected enter: %#v", calls)
		}
	}
}

func TestInjectIntoDoesNotConfirmMessageOnlyInHistory(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := injectConfirmAttempts
	injectConfirmAttempts = 1
	defer func() { injectConfirmAttempts = origAttempts }()
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", claudeEmptyComposer)
			}
			return exec.Command("echo", "hello appears in history\n"+claudeEmptyComposer)
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", ClaudeComposerClassifier, "hello", nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v, want paste confirmation failure", err)
	}
}

func TestInjectIntoDeletesNamedBufferWhenPasteFails(t *testing.T) {
	var calls [][]string
	command := func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			return exec.Command("echo", claudeEmptyComposer)
		}
		if slices.Contains(args, "paste-buffer") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	err := InjectIntoWith(context.Background(), "tmux", "%1", ClaudeComposerClassifier, "hello", nil, command)
	if err == nil {
		t.Fatal("paste failure accepted")
	}
	var buffer string
	for _, call := range calls {
		if i := slices.Index(call, "set-buffer"); i >= 0 {
			buffer = call[i+2]
		}
	}
	if buffer == "" {
		t.Fatal("set-buffer was not called")
	}
	for _, call := range calls {
		if i := slices.Index(call, "delete-buffer"); i >= 0 && slices.Contains(call, buffer) {
			return
		}
	}
	t.Fatalf("buffer %q was not deleted after paste failure: %#v", buffer, calls)
}

// TestComposerPasteConfirmedMatchesTailAcrossRowWrap proves the composer
// confirm loop matches against the JOINED, whitespace-normalized composer
// scope — not per-row HasPrefix — so a long single-line body wrapping across
// composer rows (real terminals break a line mid-word at a fixed column
// width, they don't wait for a space) still confirms. This is the fix for
// the interactive-dispatch "paste failed" regression: the opening turn joins
// a preamble and the prompt onto one long line, which wraps in the composer,
// and Enter must not fire until the tail — wherever it lands — is visible.
func TestComposerPasteConfirmedMatchesTailAcrossRowWrap(t *testing.T) {
	// No internal whitespace: the needle is now derived from the tail of the
	// whole whitespace-NORMALIZED body (submitConfirmNeedle spans lines and
	// strips spaces before slicing), so a body containing spaces would no
	// longer correspond to a contiguous raw substring the way this test's
	// hand-sliced prefix/needle split assumes.
	body := "prefixtextbeforethetailTAILMARKER1234567890AB"
	bodyRunes := []rune(body)
	needle := submitConfirmNeedle(body)
	needleRunes := []rune(needle)
	mid := len(needleRunes) / 2
	prefix := string(bodyRunes[:len(bodyRunes)-len(needleRunes)])
	// Split the needle itself across two rendered composer rows, as a
	// terminal would when the pasted text reaches its column width mid-tail.
	wrapped := string(needleRunes[:mid]) + "\n" + string(needleRunes[mid:])
	capture := "─\n❯ " + prefix + wrapped + "\n─"

	if matched, _ := composerPasteConfirmed(capture, needle, 0, 0); !matched {
		t.Fatalf("composerPasteConfirmed did not match a needle wrapped across composer rows (needle %q)", needle)
	}
}

// TestInjectIntoConfirmsWrappedTailAcrossComposerRows is the end-to-end
// version of the row-wrap regression: InjectInto's full confirm loop must
// still submit with Enter when the pasted tail lands split across two
// composer rows.
func TestInjectIntoConfirmsWrappedTailAcrossComposerRows(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	body := "prefixtextbeforethetailTAILMARKER1234567890AB"
	bodyRunes := []rune(body)
	needle := submitConfirmNeedle(body)
	needleRunes := []rune(needle)
	mid := len(needleRunes) / 2
	prefix := string(bodyRunes[:len(bodyRunes)-len(needleRunes)])
	wrapped := string(needleRunes[:mid]) + "\n" + string(needleRunes[mid:])
	wrappedComposer := "─\n❯ " + prefix + wrapped + "\n─"

	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", claudeEmptyComposer)
			}
			return exec.Command("echo", wrappedComposer)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", ClaudeComposerClassifier, body, nil); err != nil {
		t.Fatalf("InjectInto: %v (regression: composer confirm must survive a needle wrapped across rows)", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

// TestInjectIntoWithholdsEnterUntilComposerTailVisible proves head-only
// composer renders never trigger Enter: for a long single-line body, the
// early confirm-loop captures show only its HEAD (the tail-anchored needle
// is nowhere in them), and Enter must not fire until a later capture shows
// the whole body, including its tail. Asserts the full ordered tmux call
// sequence with reflect.DeepEqual: a baseline/classify capture, set-buffer,
// paste-buffer, four head-only captures that must not satisfy the confirm
// loop, one capture whose tail lands, one stability re-check, then Enter.
func TestInjectIntoWithholdsEnterUntilComposerTailVisible(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := injectConfirmAttempts
	injectConfirmAttempts = 12
	defer func() { injectConfirmAttempts = origAttempts }()

	body := "HEADSTART-" + strings.Repeat("x", 40) + "-TAILEND-DISTINCT-MARKER-1234567890"
	bodyRunes := []rune(body)
	headOnly := string(bodyRunes[:30]) // no complete tail needle of its own

	const withheldCaptures = 4
	var calls [][]string
	var buffer string
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if i := slices.Index(args, "set-buffer"); i >= 0 && i+2 < len(args) {
			buffer = args[i+2]
		}
		if slices.Contains(args, "capture-pane") {
			captures++
			switch {
			case captures == 1:
				return exec.Command("echo", claudeEmptyComposer)
			case captures <= 1+withheldCaptures:
				return exec.Command("echo", "─\n❯ "+headOnly+"\n─")
			default:
				return exec.Command("echo", "─\n❯ "+body+"\n─")
			}
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", ClaudeComposerClassifier, body, nil); err != nil {
		t.Fatalf("InjectInto: %v", err)
	}
	if buffer == "" {
		t.Fatal("set-buffer was never called")
	}

	capturePane := []string{"-L", "leo", "capture-pane", "-p", "-t", "%1"}
	cp := func() []string { c := make([]string, len(capturePane)); copy(c, capturePane); return c }
	want := [][]string{
		cp(), // baseline/classify capture
		{"-L", "leo", "set-buffer", "-b", buffer, "--", body},
		{"-L", "leo", "paste-buffer", "-b", buffer, "-d", "-p", "-t", "%1"},
		cp(), cp(), cp(), cp(), // four head-only captures: no complete tail, must not match
		cp(), // tail lands
		cp(), // stability re-check: same content, confirms
		{"-L", "leo", "send-keys", "-t", "%1", "Enter"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call sequence mismatch:\n got  %#v\nwant %#v", calls, want)
	}
}

// TestInjectIntoRequiresComposerNeedleCountToExceedBaseline proves the
// composer confirm loop, like inject.go's, never treats mere PRESENCE of the
// needle as proof the new paste landed: if the composer already carries one
// copy of the exact body before pasting even starts (a resend of an
// identical message, or the harness echoing back a prior prompt), the
// occurrence count must exceed that baseline — a pane change that leaves the
// count unchanged must not satisfy the loop, which then exhausts its budget
// and fails closed with ErrPasteFailed.
func TestInjectIntoRequiresComposerNeedleCountToExceedBaseline(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := injectConfirmAttempts
	injectConfirmAttempts = 6
	defer func() { injectConfirmAttempts = origAttempts }()

	body := "distinctive resend body text for the baseline test"
	before := "─\n❯ " + body + "\n─"
	// Differs from `before` byte-for-byte (an extra trailing blank line) so
	// the loop's after!=before guard doesn't short-circuit the test — but
	// the composer scope's needle occurrence count is still exactly 1, same
	// as the baseline: the new paste never visibly lands a SECOND copy.
	afterUnrelatedChange := before + "\n"

	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", before)
			}
			return exec.Command("echo", afterUnrelatedChange)
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, body, nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v, want paste confirmation failure (presence alone must not satisfy the confirm loop)", err)
	}
	for _, c := range calls {
		if slices.Contains(c, "send-keys") && c[len(c)-1] == "Enter" {
			t.Fatalf("unexpected Enter: %#v", calls)
		}
	}
	if captures != 1+injectConfirmAttempts {
		t.Fatalf("expected the confirm loop to exhaust its budget (%d captures after baseline), got %d: %#v", injectConfirmAttempts, captures, calls)
	}
}

// TestInjectIntoConfirmsBodyLineStartingWithGlyphLookalike proves the needle
// and the composer scope are normalized SYMMETRICALLY: a body line that
// itself starts with "› " (e.g. quoting a codex prompt, "hello\n› world")
// has that leading "› " stripped from the RENDERED composer scope by
// stripLeadingComposerGlyph (it looks exactly like real chrome), and the
// needle derived from the raw body must be stripped the same way — otherwise
// the needle keeps a "›" character the scope no longer has, and the two
// sides can never match again.
func TestInjectIntoConfirmsBodyLineStartingWithGlyphLookalike(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	body := "hello\n› world"
	// The composer renders both body lines verbatim; the second happens to
	// start with a real "› " shape, indistinguishable from actual chrome.
	rendered := "─\n❯ hello\n› world\n─"

	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", claudeEmptyComposer)
			}
			return exec.Command("echo", rendered)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", ClaudeComposerClassifier, body, nil); err != nil {
		t.Fatalf("InjectInto: %v (regression: needle must be normalized the same way as the composer scope)", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

// TestInjectIntoConfirmsOpenCodeDoubleLeadingGlyph proves
// stripLeadingComposerGlyph strips repeatedly, not just once: opencode
// renders every composer row behind its own "┃ " border, so a body line
// "› world" is rendered as "┃ › world" — TWO leading glyphs stacked, the
// border THEN the glyph-lookalike content. Stripping only the first (the
// border) leaves "› world" behind, which the scope side then normalizes to
// "›world" while the needle side (stripping "› " once) normalizes to
// "world" — permanently mismatched. Both sides must strip every leading
// glyph in a row, not just the outermost one.
func TestInjectIntoConfirmsOpenCodeDoubleLeadingGlyph(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	body := "hello\n› world"
	empty := "┃\n┃  Ask anything...\n┃\n┃  Build · model"
	rendered := "┃ hello\n┃ › world\n┃\n┃  Build · model"

	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", empty)
			}
			return exec.Command("echo", rendered)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, body, nil); err != nil {
		t.Fatalf("InjectInto: %v (regression: stripLeadingComposerGlyph must strip repeatedly, not just once)", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

// TestInjectIntoConfirmsShortBodyAgainstContaminatingHint proves a very
// short body ("a") still confirms when the composer's baseline capture is
// its empty-state hint text ("Ask Codex to do anything") — which
// coincidentally CONTAINS the 1-rune needle "a" (in "anything"), inflating
// a count-based baseline to 1. Once the hint is replaced by the actual typed
// "a", the occurrence count never exceeds that contaminated baseline under
// count-based matching, and confirmation would never succeed. Below
// submitNeedleMinRunes, InjectInto must instead use its pre-baseline,
// per-row presence check (no baseline at all), which isn't fooled by a hint
// that merely CONTAINS the needle somewhere mid-word.
func TestInjectIntoConfirmsShortBodyAgainstContaminatingHint(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	hint := "─\n❯ Ask Codex to do anything\n─"
	landed := "─\n❯ a\n─"
	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", hint)
			}
			return exec.Command("echo", landed)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "a", nil); err != nil {
		t.Fatalf("InjectInto: %v (regression: short-body confirm must not be contaminated by the composer's empty-state hint text)", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

// TestInjectIntoConfirmsShortBodyOkAgainstContaminatingHint is the 2-rune
// sibling of the above: body "ok" against a hint whose text happens to
// CONTAIN "ok" as a substring ("bookmark").
func TestInjectIntoConfirmsShortBodyOkAgainstContaminatingHint(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	hint := "─\n❯ Ask Codex to bookmark this\n─"
	landed := "─\n❯ ok\n─"
	captures := 0
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", hint)
			}
			return exec.Command("echo", landed)
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "ok", nil); err != nil {
		t.Fatalf("InjectInto: %v (regression: short-body confirm must not be contaminated by the composer's empty-state hint text)", err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

func TestInjectIntoPasteFailedNoEnter(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := submitConfirmAttempts
	submitConfirmAttempts = 1
	defer func() { submitConfirmAttempts = origAttempts }()
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("echo", "empty")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v", err)
	}
	for _, c := range calls {
		if slices.Contains(c, "Enter") {
			t.Fatalf("unexpected Enter: %#v", calls)
		}
	}
}
