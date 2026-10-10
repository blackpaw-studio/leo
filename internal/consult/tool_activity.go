package consult

import (
	"maps"
	"time"
)

// applyToolEvent returns open updated for one tool hook (event is the
// normalized hook name): PreToolUse adds the call, PostToolUse and
// PostToolUseFailure remove it. open is never mutated. A payload without a
// tool_use_id cannot be matched to its settling event, so it is not tracked.
func applyToolEvent(open map[string]time.Time, event, toolUseID string, now time.Time) map[string]time.Time {
	if toolUseID == "" {
		return open
	}
	if event == "pretooluse" {
		next := maps.Clone(open)
		if next == nil {
			next = map[string]time.Time{}
		}
		if _, tracked := next[toolUseID]; !tracked {
			next[toolUseID] = now
		}
		return next
	}
	if _, tracked := open[toolUseID]; !tracked {
		return open
	}
	next := maps.Clone(open)
	delete(next, toolUseID)
	if len(next) == 0 {
		return nil
	}
	return next
}
