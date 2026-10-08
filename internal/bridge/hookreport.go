package bridge

import "encoding/json"

// hookEventNames maps bridge events to the hook_event_name Claude Code's own
// shell hooks post for the same moment.
var hookEventNames = map[string]string{
	EventTurnStart:    "UserPromptSubmit",
	EventTurnComplete: "Stop",
	EventSessionEnd:   "SessionEnd",
}

// TurnAborted is turn.complete's reason when the turn was interrupted
// rather than finished.
const TurnAborted = "aborted"

// IsFinalSessionEnd reports whether a session.end with reason leaves the
// claude process for good: not a /clear or a resume, after which the same
// process goes on under another session.
func IsFinalSessionEnd(reason string) bool { return reason != "clear" && reason != "resume" }

// bridgeEventIDPrefix keeps bridge event ids apart from shell-hook ones in
// the dispatcher's shared dedup set.
const bridgeEventIDPrefix = "bridge:"

// HookPayload translates a bridge event into the report the claude shell
// turn hooks post to /api/dispatch/{id}/report (consult.HookReport's EventID
// and Payload), so a bridge event can drive consult.Dispatcher.Report
// unchanged. ok is false for events with no hook counterpart (hello). An
// aborted turn.complete becomes an Interrupt, which Claude's shell hooks
// never report, so an interrupted turn closes as interrupted.
//
// turn.start's prompt and turn.complete's final message travel under the
// shell hooks' own keys (prompt, last_assistant_message), as does
// turn.complete's pending work (background_tasks, session_crons), so a late-acked
// orchestrator turn is matched by its text (the prompt as leo sent it, out
// of the envelope Claude wraps a non-user deliver in) and a dispatch's result is the
// subagent's final message, exactly as on the hook path. Unlike the claude
// shell hooks, which identify a turn by prompt_id, the payload names the
// engine's turn (bridge_turn_id) on both events, and turn.start names the
// command and origin that submitted it (command_id, origin), so the
// dispatcher attributes each Stop to its own turn exactly.
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
	if ev.Name == EventTurnComplete && ev.Reason == TurnAborted {
		// The dispatcher's own name for a turn that ended early.
		hookName = "Interrupt"
	}
	fields := map[string]any{"hook_event_name": hookName}
	if ev.SessionID != "" {
		fields["session_id"] = ev.SessionID
	}
	if (ev.Name == EventTurnStart || ev.Name == EventTurnComplete) && ev.TurnID != "" {
		fields["bridge_turn_id"] = ev.TurnID
	}
	if ev.Name == EventTurnStart {
		if ev.CommandID != "" {
			fields["command_id"] = ev.CommandID
		}
		if ev.Origin != "" {
			fields["origin"] = ev.Origin
		}
	}
	switch {
	case ev.Name == EventTurnStart && ev.Prompt != "":
		fields["prompt"] = UnwrapPluginPrompt(ev.Prompt)
	case ev.Name == EventTurnComplete && ev.Message != "":
		fields["last_assistant_message"] = ev.Message
	case ev.Name == EventSessionEnd && ev.Reason != "":
		fields["reason"] = ev.Reason
	}
	if ev.Name == EventTurnComplete && ev.Pending != nil {
		fields["background_tasks"], fields["session_crons"] = ev.Pending.stopHookFields()
	}
	raw, err := json.Marshal(fields)
	if err != nil { // unreachable for strings and string maps
		return "", nil, false
	}
	if ev.EventID != "" {
		eventID = bridgeEventIDPrefix + ev.EventID
	}
	return eventID, raw, true
}
