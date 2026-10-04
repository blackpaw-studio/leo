package bridge

import "encoding/json"

// hookEventNames maps bridge events to the hook_event_name Claude Code's own
// shell hooks post for the same moment.
var hookEventNames = map[string]string{
	EventTurnStart:    "UserPromptSubmit",
	EventTurnComplete: "Stop",
	EventSessionEnd:   "SessionEnd",
}

// bridgeEventIDPrefix keeps bridge event ids apart from shell-hook ones in
// the dispatcher's shared dedup set.
const bridgeEventIDPrefix = "bridge:"

// HookPayload translates a bridge event into the report the claude shell
// turn hooks post to /api/dispatch/{id}/report (consult.HookReport's EventID
// and Payload), so a bridge event can drive consult.Dispatcher.Report
// unchanged. ok is false for events with no hook counterpart (hello).
//
// turn.start's prompt and turn.complete's final message travel under the
// shell hooks' own keys (prompt, last_assistant_message), so a late-acked
// orchestrator turn is matched by its text and a dispatch's result is the
// subagent's final message, exactly as on the hook path. Like the claude
// shell hooks, the payload carries no turn id: Claude can drain queued
// prompts inside one running turn, and an id-less Stop closes them all.
//
// The mod retries a report the daemon may already have applied (its reply
// lost); eventID, derived from the event's stable id, lets the dispatcher
// drop the replay. A replayed id-less Stop would otherwise close the next
// queued turn.
func HookPayload(ev Event) (eventID string, payload json.RawMessage, ok bool) {
	hookName, ok := hookEventNames[ev.Name]
	if !ok {
		return "", nil, false
	}
	fields := map[string]string{"hook_event_name": hookName}
	if ev.SessionID != "" {
		fields["session_id"] = ev.SessionID
	}
	switch {
	case ev.Name == EventTurnStart && ev.Prompt != "":
		fields["prompt"] = ev.Prompt
	case ev.Name == EventTurnComplete && ev.Message != "":
		fields["last_assistant_message"] = ev.Message
	case ev.Name == EventSessionEnd && ev.Reason != "":
		fields["reason"] = ev.Reason
	}
	raw, err := json.Marshal(fields)
	if err != nil { // unreachable for a map of strings
		return "", nil, false
	}
	if ev.EventID != "" {
		eventID = bridgeEventIDPrefix + ev.EventID
	}
	return eventID, raw, true
}
