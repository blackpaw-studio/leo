package bridge

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// hookEventNames maps bridge events to the hook_event_name Claude Code's own
// shell hooks post for the same moment.
var hookEventNames = map[string]string{
	EventTurnStart:    "UserPromptSubmit",
	EventTurnComplete: "Stop",
	EventSessionEnd:   "SessionEnd",
}

// HookReport translates a bridge event into the consult.HookReport the
// claude shell turn hooks post to /api/dispatch/{id}/report, so a bridge
// event can drive consult.Dispatcher.Report unchanged. ok is false for
// events with no hook counterpart (hello).
//
// The payload carries no turn id and no prompt or last-assistant text,
// because the bridge protocol has none: turn.start therefore matches only
// an armed orchestrator turn, and turn.complete closes the working turns
// without recording their text. EventID stays empty — each bridge event is
// posted exactly once, so there is no retry to deduplicate.
func HookReport(ev Event) (consult.HookReport, bool) {
	hookName, ok := hookEventNames[ev.Name]
	if !ok {
		return consult.HookReport{}, false
	}
	payload := map[string]string{"hook_event_name": hookName}
	if ev.SessionID != "" {
		payload["session_id"] = ev.SessionID
	}
	if ev.Name == EventSessionEnd && ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	raw, err := json.Marshal(payload)
	if err != nil { // unreachable for a map of strings
		return consult.HookReport{}, false
	}
	return consult.HookReport{Payload: raw}, true
}
