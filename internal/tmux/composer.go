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
	composerDialogPattern = regexp.MustCompile(`(?i)\b(trust|permission|update|hook review|approve|allow|deny)\b`)
	composerBusyPattern   = regexp.MustCompile(`(?i)\b(working|thinking|generating|running)\b|esc\s+(?:to\s+)?interrupt`)
)

// CodexComposerClassifier identifies Codex's › composer. Codex renders its
// empty-composer placeholder on the same line as the marker.
func CodexComposerClassifier(capture string) ComposerState {
	return classifyComposer(capture, "› ", isCodexPlaceholder)
}

// ClaudeComposerClassifier identifies Claude Code's ❯ composer. Claude
// normally renders an empty composer as a bare glyph, but accepts its prompt
// hint too because versions differ in whether that hint is visible.
func ClaudeComposerClassifier(capture string) ComposerState {
	// capture-pane may trim the trailing space from an otherwise empty prompt.
	return classifyComposer(capture, "❯", isClaudePlaceholder)
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
	return HasConfirmFooterLine(capture) || composerDialogPattern.MatchString(capture)
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
		if strings.HasPrefix(line, "✻") || strings.HasPrefix(line, "✽") ||
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
