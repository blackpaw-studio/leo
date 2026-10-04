package bridge

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The translation must reproduce what Claude Code's own shell hooks post to
// /api/dispatch/{id}/report today (hook_event_name spelled as Claude spells
// it, no turn id), so consult.Dispatcher.Report needs no bridge awareness.
func TestHookReportTranslation(t *testing.T) {
	cases := []struct {
		name   string
		ev     Event
		wantOK bool
		want   map[string]any
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
			got, ok := HookReport(tc.ev)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.EventID != "" {
				t.Fatalf("EventID=%q; bridge reports are posted once, so no dedup id is needed", got.EventID)
			}
			var payload map[string]any
			if err := json.Unmarshal(got.Payload, &payload); err != nil {
				t.Fatalf("payload is not a JSON object: %v (%s)", err, got.Payload)
			}
			if !reflect.DeepEqual(payload, tc.want) {
				t.Fatalf("payload=%v, want %v", payload, tc.want)
			}
		})
	}
}
