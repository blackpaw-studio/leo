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

	// Only a spinner immediately before the active composer box means Claude is
	// busy. Finished-turn summaries and prompt history are above that boundary.
	for _, line := range lines[:top] {
		if isClaudeBusyLine(line) {
			return ComposerBusy
		}
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

func isClaudeBusyLine(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "✻") {
		if strings.Contains(line, "· done") {
			return false
		}
		return isClaudeInProgressLine(line)
	}
	if strings.HasPrefix(line, "✽") || strings.HasPrefix(line, "✶") || strings.HasPrefix(line, "✳") ||
		strings.HasPrefix(line, "✢") || strings.HasPrefix(line, "⠋") {
		return true
	}
	return composerBusyPattern.MatchString(line)
}

func isClaudeInProgressLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(trimmed, "…") || strings.HasSuffix(trimmed, "...") ||
		strings.Contains(lower, "esc to interrupt") || strings.Contains(lower, "(thinking)") ||
		strings.ContainsAny(trimmed, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
}

func classifyComposer(capture, marker string, placeholder func(string) bool) ComposerState {
	if strings.TrimSpace(capture) == "" || isComposerDialog(capture) {
		return ComposerUnknown
	}
	if hasBusyIndicator(capture) {
		return ComposerBusy
	}

	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(line, marker) {
			continue
		}

		content := strings.TrimSpace(line[len(marker):])
		if menuOptionPattern.MatchString(content) {
			return ComposerUnknown
		}
		if content == "" || placeholder(content) {
			return ComposerEmpty
		}
		return ComposerDraft
	}

	return ComposerUnknown
}

func isComposerDialog(capture string) bool {
	if HasConfirmFooterLine(capture) {
		return true
	}
	for _, line := range strings.Split(capture, "\n") {
		line = strings.TrimSpace(line)
		if menuOptionPattern.MatchString(line) || composerDialogTitlePattern.MatchString(line) {
			return true
		}
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
