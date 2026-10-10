package consult

import (
	"encoding/json"
	"maps"
	"strings"
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

// IsToolActivityReport reports whether a hook payload is a tool lifecycle
// event, which is activity only: it carries no turn or session meaning, so a
// bridge that owns turn state still wants it applied.
func IsToolActivityReport(payload []byte) bool {
	var p struct {
		Event string `json:"hook_event_name"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return false
	}
	return isToolActivityEvent(normalizeHookEvent(p.Event))
}

func isToolActivityEvent(event string) bool {
	return event == "pretooluse" || event == "posttooluse" || event == "posttoolusefailure"
}

func normalizeHookEvent(event string) string {
	return strings.ToLower(strings.ReplaceAll(event, "_", ""))
}
