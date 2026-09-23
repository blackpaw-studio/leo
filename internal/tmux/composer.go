package tmux

import (
	"regexp"
	"strings"
)

// ComposerState is the visible state of a harness's interactive composer.
// It is deliberately based only on capture-pane output: classifiers never
// send input to discover state.
type ComposerState uint8

const (
	ComposerBusy ComposerState = iota
	ComposerEmpty
	ComposerDraft
	ComposerUnknown
)

func (s ComposerState) String() string {
	switch s {
	case ComposerBusy:
		return "busy"
	case ComposerEmpty:
		return "empty"
	case ComposerDraft:
		return "draft"
	default:
		return "unknown"
	}
}

// ComposerClassifier classifies one passive tmux capture.
type ComposerClassifier func(capture string) ComposerState

var (
	composerDialogTitlePattern = regexp.MustCompile(`(?i)^(?:(?:trust|permission|update|hook review|approve|allow|deny)(?:[[:punct:]\s]+|$))+$`)
	composerBusyPattern        = regexp.MustCompile(`(?i)\b(working|thinking|generating|running)\b|esc\s+(?:to\s+)?interrupt`)
	claudeRulePattern          = regexp.MustCompile(`^─+`)
)

// CodexComposerClassifier identifies Codex's › composer. Codex renders its
// empty-composer placeholder on the same line as the marker.
func CodexComposerClassifier(capture string) ComposerState {
	// capture-pane drops the trailing space from a bare `› ` input line.
	return classifyComposer(capture, "›", isCodexPlaceholder)
}

// ClaudeComposerClassifier identifies Claude Code's ❯ composer. Claude
// normally renders an empty composer as a bare glyph, but accepts its prompt
// hint too because versions differ in whether that hint is visible.
func ClaudeComposerClassifier(capture string) ComposerState {
	if strings.TrimSpace(capture) == "" || isClaudeDialog(capture) {
		return ComposerUnknown
	}

	lines := strings.Split(capture, "\n")
	top, composerLine, bottom, ok := claudeComposerBox(lines)
	if !ok {
		if hasBusyIndicator(capture) {
			return ComposerBusy
		}
		return ComposerUnknown
	}

	if isClaudeStatusBusy(lines[:top]) {
		return ComposerBusy
	}

	composer := strings.TrimLeft(lines[composerLine], " \t")
	content := strings.TrimSpace(strings.TrimPrefix(composer, "❯"))
	if content != "" && !isClaudePlaceholder(content) {
		return ComposerDraft
	}
	for _, line := range lines[composerLine+1 : bottom] {
		if strings.TrimSpace(line) != "" {
			return ComposerDraft
		}
	}
	return ComposerEmpty
}

// claudeComposerBox finds the final ruled composer box. Its footer is outside
// the box, so it must never influence composer classification.
func claudeComposerBox(lines []string) (int, int, int, bool) {
	for bottom := len(lines) - 1; bottom >= 0; bottom-- {
		if !claudeRulePattern.MatchString(strings.TrimSpace(lines[bottom])) {
			continue
		}
		for top := bottom - 1; top >= 0; top-- {
			if !claudeRulePattern.MatchString(strings.TrimSpace(lines[top])) {
				continue
			}
			for composerLine := bottom - 1; composerLine > top; composerLine-- {
				composer := strings.TrimLeft(lines[composerLine], " \t")
				if strings.HasPrefix(composer, "❯") {
					return top, composerLine, bottom, true
				}
			}
			break
		}
	}
	return 0, 0, 0, false
}

func isClaudeDialog(capture string) bool {
	return isComposerDialog(capture)
}

// isClaudeStatusBusy reports whether the live status region directly above
// the composer box shows an in-progress spinner. Claude renders that spinner
// (or its "done" summary) below the last transcript message, so the walk stops
// at the nearest spinner line or at the ⏺ message that starts the transcript.
// Queued prompts may render between the spinner and the box, so ❯ is not a
// boundary. Transcript prose never counts, whatever words it contains.
func isClaudeStatusBusy(above []string) bool {
	for i := len(above) - 1; i >= 0; i-- {
		if isClaudeSpinnerLine(above[i]) {
			return isClaudeBusyLine(above[i])
		}
		if strings.HasPrefix(strings.TrimSpace(above[i]), "⏺") {
			return false
		}
	}
	return false
}

// Claude's spinner cycles · ✢ ✳ ✶ ✻ ✽ (* substitutes for ✳ off darwin).
var (
	claudeSpinnerGlyphs     = []string{"✻", "✽", "✶", "✳", "✢", "⠋"}
	claudeWeakSpinnerGlyphs = []string{"·", "*"}
	claudeTokenCounter      = regexp.MustCompile(`\(\s*\d+[hms][^)]*[↑↓]`)
)

// isClaudeSpinnerLine recognizes a spinner frame. The · and * frames double as
// bullets, so they count only unindented and in the in-progress shape.
func isClaudeSpinnerLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, glyph := range claudeSpinnerGlyphs {
		if strings.HasPrefix(trimmed, glyph) {
			return true
		}
	}
	for _, glyph := range claudeWeakSpinnerGlyphs {
		if strings.HasPrefix(line, glyph+" ") {
			return isClaudeInProgressLine(line)
		}
	}
	return false
}

// isClaudeBusyLine classifies one spinner line: "✻ Worked for 3s · done" is a
// finished-turn summary, every other spinner frame is live.
func isClaudeBusyLine(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "✻") {
		return isClaudeSpinnerLine(line)
	}
	if strings.Contains(line, "· done") {
		return false
	}
	return isClaudeInProgressLine(line)
}

func isClaudeInProgressLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(trimmed, "…") || strings.HasSuffix(trimmed, "...") ||
		strings.Contains(lower, "esc to interrupt") || strings.Contains(lower, "(thinking)") ||
		claudeTokenCounter.MatchString(trimmed) ||
		strings.ContainsAny(trimmed, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
}

func classifyComposer(capture, marker string, placeholder func(string) bool) ComposerState {
	if strings.TrimSpace(capture) == "" || isComposerDialog(capture) {
		return ComposerUnknown
	}
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(line, marker) {
			continue
		}

		// With a composer on screen, only its live status line can mean busy;
		// agent messages above it are prose.
		if isStatusLineBusy(lines[:i]) {
			return ComposerBusy
		}
		content := strings.TrimSpace(line[len(marker):])
		if content == "" || placeholder(content) {
			return ComposerEmpty
		}
		return ComposerDraft
	}

	if hasBusyIndicator(capture) {
		return ComposerBusy
	}
	return ComposerUnknown
}

var codexStatusLinePattern = regexp.MustCompile(`^•\s+\S+(?:\s+\S+){0,3}\s+\(\d+[hms]`)

// isStatusLineBusy scans the region between a composer and the transcript
// entry above it (the previous • message or › prompt). Codex renders its live
// "• Working (12s • esc to interrupt)" status there, followed by queued ↳ rows
// and their edit hint; any of those may wrap in a narrow pane, so the region is
// joined before matching. A finished agent message carries neither shape.
func isStatusLineBusy(above []string) bool {
	region := []string{}
	for i := len(above) - 1; i >= 0; i-- {
		line := strings.TrimSpace(above[i])
		region = append([]string{line}, region...)
		if strings.HasPrefix(line, "•") || strings.HasPrefix(line, "›") {
			break
		}
	}
	if len(region) > 0 && codexStatusLinePattern.MatchString(region[0]) {
		return true
	}
	joined := strings.ToLower(strings.Join(strings.Fields(strings.Join(region, " ")), " "))
	return strings.Contains(joined, "esc to interrupt")
}

func isComposerDialog(capture string) bool {
	lines := strings.Split(capture, "\n")
	return hasComposerDialogFooterAtBottom(lines) || hasComposerMenuBlock(lines)
}

// hasComposerMenuBlock recognizes a live selection menu, not a numbered line
// that happened to appear in transcript history. A menu has the active
// selector on one option and at least one immediately-following option aligned
// at the same content column. Unnumbered menus also need a dedicated
// continuation footer, since a wrapped composer draft has the same alignment.
func hasComposerMenuBlock(lines []string) bool {
	i := lastComposerSelectorLine(lines)
	if i < 0 || i+1 >= len(lines) {
		return false
	}
	contentIndent, content, ok := selectedMenuOption(lines[i])
	if !ok {
		return false
	}

	nextIndent, nextContent := menuOptionLine(lines[i+1])
	if nextContent == "" || nextIndent != contentIndent || !isComposerMenuOption(nextContent) {
		return false
	}
	if menuOptionPattern.MatchString(content) && menuOptionPattern.MatchString(nextContent) {
		return true
	}
	return hasMenuContinueFooterAfter(lines, i+2)
}

func lastComposerSelectorLine(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if strings.HasPrefix(line, "›") || strings.HasPrefix(line, "❯") {
			return i
		}
	}
	return -1
}

func isComposerMenuOption(content string) bool {
	// Status/footer rows share an option's indentation in Codex, but carry the
	// middle-dot-separated model/context metadata that menu choices do not.
	return !strings.Contains(content, "·")
}

func selectedMenuOption(line string) (int, string, bool) {
	indent, rest := menuOptionLine(line)
	for _, marker := range []string{"›", "❯"} {
		if !strings.HasPrefix(rest, marker) {
			continue
		}
		afterMarker := rest[len(marker):]
		spacing := len(afterMarker) - len(strings.TrimLeft(afterMarker, " \t"))
		content := strings.TrimSpace(afterMarker)
		if spacing == 0 || content == "" {
			return 0, "", false
		}
		// Both selector glyphs occupy one terminal column; spaces after the
		// glyph align the next option's indentation with its content.
		return indent + 1 + spacing, content, true
	}
	return 0, "", false
}

func menuOptionLine(line string) (int, string) {
	indent := len(line) - len(strings.TrimLeft(line, " \t"))
	return indent, strings.TrimSpace(line)
}

func hasMenuContinueFooterAfter(lines []string, menuEnd int) bool {
	if menuEnd >= len(lines) {
		return false
	}
	footerEnd := min(menuEnd+3, len(lines))
	return dialogFooterLineContaining(strings.Join(lines[menuEnd:footerEnd], "\n"), "Enter to continue")
}

func hasComposerDialogFooterAtBottom(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		footer := strings.TrimSpace(lines[i])
		if footer == "" {
			continue
		}
		if HasConfirmFooterLine(footer) {
			return true
		}
		if dialogFooterLineContaining(footer, "Enter to continue") {
			return hasComposerDialogTitleBefore(lines, i)
		}
		return false
	}
	return false
}

func hasComposerDialogTitleBefore(lines []string, footer int) bool {
	for i := footer - 1; i >= 0 && i >= footer-3; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return composerDialogTitlePattern.MatchString(line)
	}
	return false
}

func isCodexPlaceholder(content string) bool {
	return content == "Ask Codex to do anything"
}

func isClaudePlaceholder(content string) bool {
	return content == "Type a message…" || content == "Type a message..."
}

func hasBusyIndicator(capture string) bool {
	for _, line := range strings.Split(capture, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(line), "esc to interrupt") {
			return true
		}
		if strings.HasPrefix(line, "✻") {
			if isClaudeBusyLine(line) {
				return true
			}
			continue
		}
		if strings.HasPrefix(line, "✽") ||
			strings.HasPrefix(line, "✶") || strings.HasPrefix(line, "✳") ||
			strings.HasPrefix(line, "✢") {
			return true
		}
		if strings.HasPrefix(line, "• ") && composerBusyPattern.MatchString(line) {
			return true
		}
	}
	return false
}
