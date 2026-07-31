package observe

import (
	"context"
	"os/exec"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// captureExecCommand is the seam tests replace for tmux capture-pane.
var captureExecCommand = exec.CommandContext

// ansiEscapeRegexp matches CSI-style ANSI escape sequences (color, cursor
// movement, etc.) as emitted by an interactive TUI.
var ansiEscapeRegexp = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// capturePaneAction captures the tail of an agent's tmux pane and derives a
// best-effort CurrentAction from it. Returns nil on any failure or when the
// pane has no non-empty content — never a stable field to parse, per
// docs/specs/2026-07-31-observability-api.md.
func capturePaneAction(ctx context.Context, tmuxPath, sessionName string) *Action {
	out, err := captureExecCommand(ctx, tmuxPath, tmux.Args("capture-pane", "-t", tmux.Target(sessionName), "-p")...).Output()
	if err != nil {
		return nil
	}
	detail := sanitizePaneLine(lastNonEmptyLine(string(out)))
	if detail == "" {
		return nil
	}
	return &Action{Kind: ActionKindPane, Detail: detail}
}

// lastNonEmptyLine returns the last line of s that isn't blank once
// trimmed, or "" if every line is blank.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// sanitizePaneLine strips ANSI escapes and control characters, collapses
// whitespace, and truncates to MaxActionDetail runes (never mid-rune) — the
// display-text contract Action.Detail promises consumers.
func sanitizePaneLine(s string) string {
	s = ansiEscapeRegexp.ReplaceAllString(s, "")

	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}

	collapsed := strings.Join(strings.Fields(b.String()), " ")
	return truncateRunes(collapsed, MaxActionDetail)
}

// truncateRunes truncates s to at most max runes, never splitting a
// multi-byte rune.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
