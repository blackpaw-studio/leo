package tmux

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// activityExecCommand is the seam tests replace.
var activityExecCommand = exec.CommandContext

// SessionActivity is the liveness metadata the idle-suspend sweep needs for one
// tmux session: how many clients are attached and when the session was last
// active (tmux's session_activity, which advances on injected input,
// interactive typing in an attached pane, and the pane's own output).
type SessionActivity struct {
	Attached     int
	LastActivity time.Time
}

// ListSessionActivity returns per-session activity for every session on Leo's
// tmux server, keyed by session name. One `list-sessions` call serves a whole
// sweep. A dead/absent server ("no server running") yields an empty map and a
// nil error — the sweep treats that as "nothing to suspend".
func ListSessionActivity(ctx context.Context, tmuxPath string) (map[string]SessionActivity, error) {
	const format = "#{session_name}|#{session_attached}|#{session_activity}"
	out, err := activityExecCommand(ctx, tmuxPath, Args("list-sessions", "-F", format)...).Output()
	if err != nil {
		// `tmux list-sessions` exits non-zero when no server is running.
		// Best-effort: report no sessions rather than an error.
		return map[string]SessionActivity{}, nil
	}
	return parseSessionActivity(string(out)), nil
}

// parseSessionActivity parses the `name|attached|epoch` lines emitted by
// ListSessionActivity. Malformed lines and unparseable epochs are skipped.
func parseSessionActivity(out string) map[string]SessionActivity {
	result := make(map[string]SessionActivity)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		epoch, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil {
			continue
		}
		attached, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		result[parts[0]] = SessionActivity{
			Attached:     attached,
			LastActivity: time.Unix(epoch, 0),
		}
	}
	return result
}
