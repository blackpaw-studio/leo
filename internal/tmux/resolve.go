package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ResolvePane finds the concrete pane hosting a session's harness TUI. If
// anything splits the session's window — a human attaching and splitting, or
// a TUI feature spawning its own split — the session's active pane (what
// PaneTarget resolves to) can drift away from the pane the harness is
// actually running in, so injected keystrokes land in the wrong place.
//
// tmux pane IDs are server-global and assigned in creation order, so the
// lowest-numbered pane in a session is the one the harness was originally
// started in, regardless of which pane later became active. ResolvePane
// lists every pane in session and returns that lowest pane ID (e.g. "%3").
func ResolvePane(ctx context.Context, tmuxPath, session string) (string, error) {
	target := PaneTarget(session)
	out, err := execCommand(ctx, tmuxPath, Args("list-panes", "-t", target, "-F", "#{pane_id}")...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux list-panes -t %q: %w", target, err)
	}

	lowestID := ""
	lowestNum := 0
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num, err := strconv.Atoi(strings.TrimPrefix(line, "%"))
		if !strings.HasPrefix(line, "%") || err != nil {
			return "", fmt.Errorf("tmux list-panes -t %q: unparsable pane id %q", target, line)
		}
		if lowestID == "" || num < lowestNum {
			lowestID = line
			lowestNum = num
		}
	}
	if lowestID == "" {
		return "", fmt.Errorf("tmux list-panes -t %q: no panes found", target)
	}
	return lowestID, nil
}
