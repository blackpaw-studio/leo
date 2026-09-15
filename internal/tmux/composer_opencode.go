package tmux

import "strings"

// OpenCodeComposerClassifier passively recognizes OpenCode's bordered input
// panel. It deliberately rejects captures without the footer: a history line
// beginning with the same border glyph is not sufficient evidence.
func OpenCodeComposerClassifier(capture string) ComposerState {
	if strings.TrimSpace(capture) == "" || isComposerDialog(capture) {
		return ComposerUnknown
	}
	if hasBusyIndicator(capture) {
		return ComposerBusy
	}
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "┃") || !strings.Contains(line, "·") {
			continue
		}
		for j := i - 1; j >= 0 && i-j <= 4; j-- {
			content := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[j]), "┃"))
			if content == "" {
				continue
			}
			if strings.HasPrefix(content, "Ask anything...") {
				return ComposerEmpty
			}
			return ComposerDraft
		}
		return ComposerUnknown
	}
	return ComposerUnknown
}

func openCodeComposerLines(lines []string) ([]string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "┃") && strings.Contains(line, "·") {
			start := i
			for start > 0 && i-start < 4 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "┃") {
				start--
			}
			return lines[start:i], true
		}
	}
	return nil, false
}
