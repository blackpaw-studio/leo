package bridge

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The translation must reproduce what Claude Code's own shell hooks post to
// /api/dispatch/{id}/report today (hook_event_name spelled as Claude spells
// it, the prompt and last_assistant_message under the hook's own keys; the
// bridge's turn id travels as bridge_turn_id), so consult.Dispatcher.Report
// needs little bridge awareness.
func TestHookPayloadTranslation(t *testing.T) {
	cases := []struct {
		name        string
		ev          Event
		wantOK      bool
		want        map[string]any
		wantEventID string
	}{
		{
			name:   "turn.start is UserPromptSubmit",
			ev:     Event{Agent: agentA, Name: EventTurnStart, SessionID: "s-1"},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s-1"},
		},
		{
			name:   "turn.complete is Stop",
			ev:     Event{Agent: agentA, Name: EventTurnComplete, SessionID: "s-1", Usage: json.RawMessage(`{"u":1}`)},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "Stop", "session_id": "s-1"},
		},
		{
			name:   "session.end is SessionEnd with its reason",
			ev:     Event{Agent: agentA, Name: EventSessionEnd, SessionID: "s-1", Reason: "prompt_input_exit"},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "SessionEnd", "session_id": "s-1", "reason": "prompt_input_exit"},
		},
		{
			name:   "turn.start carries a framed deliver as leo sent it, not as Claude wrapped it",
			ev:     Event{Agent: agentA, Name: EventTurnStart, SessionID: "s-1", Prompt: pluginWrapped("From orch via leo:\n\nnext")},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s-1", "prompt": "From orch via leo:\n\nnext"},
		},
		{
			name:   "turn.start carries the prompt like the shell hook",
			ev:     Event{Agent: agentA, Name: EventTurnStart, SessionID: "s-1", Prompt: "brief text"},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s-1", "prompt": "brief text"},
		},
		{
			name:   "turn.complete carries the final message as last_assistant_message",
			ev:     Event{Agent: agentA, Name: EventTurnComplete, SessionID: "s-1", Message: "final words"},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "Stop", "session_id": "s-1", "last_assistant_message": "final words"},
		},
		{
			name:   "an aborted turn.complete is an Interrupt, keeping its partial message",
			ev:     Event{Agent: agentA, Name: EventTurnComplete, SessionID: "s-1", Reason: TurnAborted, Message: "partial"},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "Interrupt", "session_id": "s-1", "last_assistant_message": "partial"},
		},
		{
			name:        "the event id becomes the report's dedup id",
			ev:          Event{Agent: agentA, Name: EventTurnComplete, SessionID: "s-1", EventID: "turn.complete:t1"},
			wantOK:      true,
			want:        map[string]any{"hook_event_name": "Stop", "session_id": "s-1"},
			wantEventID: "bridge:turn.complete:t1",
		},
		{
			name:   "session id omitted before any hello",
			ev:     Event{Agent: agentA, Name: EventTurnStart},
			wantOK: true,
			want:   map[string]any{"hook_event_name": "UserPromptSubmit"},
		},
		{
			name:   "hello has no hook counterpart",
			ev:     Event{Agent: agentA, Name: ReportHello, SessionID: "s-1"},
			wantOK: false,
		},
		{
			name:   "unknown event has no hook counterpart",
			ev:     Event{Agent: agentA, Name: "turn.middle"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventID, raw, ok := HookPayload(tc.ev)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if eventID != tc.wantEventID {
				t.Fatalf("eventID=%q, want %q", eventID, tc.wantEventID)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("payload is not a JSON object: %v (%s)", err, raw)
			}
			if !reflect.DeepEqual(payload, tc.want) {
				t.Fatalf("payload=%v, want %v", payload, tc.want)
			}
		})
	}
}
