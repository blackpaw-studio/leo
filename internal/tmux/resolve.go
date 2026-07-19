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
	pane, err := LowestPaneID(string(out))
	if err != nil {
		return "", fmt.Errorf("tmux list-panes -t %q: %w", target, err)
	}
	return pane, nil
}

// ResolvePaneOrFallback is ResolvePane for best-effort callers: it resolves
// session's concrete pane, falling back to PaneTarget(session)'s active-pane
// selector if resolution fails, rather than propagating an error. Use this
// for sites that must stay best-effort (never error louder than before
// ResolvePane existed) — e.g. dev-channel accept, startup-dialog dismiss,
// abort.
func ResolvePaneOrFallback(ctx context.Context, tmuxPath, session string) string {
	if pane, err := ResolvePane(ctx, tmuxPath, session); err == nil {
		return pane
	}
	return PaneTarget(session)
}

// LowestPaneID parses list-panes -F "#{pane_id}" output (one pane id per
// line, e.g. "%12\n%3\n%25") and returns the lowest-numbered id — the
// server-global, creation-ordered pane the harness was originally started
// in. Exported so callers with their own exec seam that can't share
// ResolvePane's internal one (e.g. package web, whose testable exec seam
// takes no context.Context) can run list-panes themselves and reuse this
// selection logic instead of duplicating it. Errors on empty input or an
// unparsable line.
func LowestPaneID(output string) (string, error) {
	lowestID := ""
	lowestNum := 0
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num, err := strconv.Atoi(strings.TrimPrefix(line, "%"))
		if !strings.HasPrefix(line, "%") || err != nil {
			return "", fmt.Errorf("unparsable pane id %q", line)
		}
		if lowestID == "" || num < lowestNum {
			lowestID = line
			lowestNum = num
		}
	}
	if lowestID == "" {
		return "", fmt.Errorf("no panes found")
	}
	return lowestID, nil
}
