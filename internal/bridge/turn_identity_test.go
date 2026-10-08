package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A turn's identity travels with its events: turn.start names the turn and,
// when the mod submitted it for a command, which command and who submitted;
// turn.complete names the turn it ends. The dispatcher attributes a Stop to
// its turn by that id, never by arrival order.
func TestParseReportCarriesTurnIdentity(t *testing.T) {
	got, err := ParseReport([]byte(`{"type":"event","name":"turn.start","turn_id":"t-1","command_id":"c-9","origin":"plugin"}`))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.TurnID != "t-1" || got.CommandID != "c-9" || got.Origin != "plugin" {
		t.Fatalf("turn.start identity = %q/%q/%q", got.TurnID, got.CommandID, got.Origin)
	}
	got, err = ParseReport([]byte(`{"type":"event","name":"turn.complete","turn_id":"t-1"}`))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.TurnID != "t-1" {
		t.Fatalf("turn.complete turn id = %q", got.TurnID)
	}
}

func TestParseReportRejectsMisplacedOrMalformedTurnIdentity(t *testing.T) {
	cases := []struct{ name, body string }{
		{"command_id on turn.complete", `{"type":"event","name":"turn.complete","turn_id":"t-1","command_id":"c"}`},
		{"origin on turn.complete", `{"type":"event","name":"turn.complete","turn_id":"t-1","origin":"plugin"}`},
		{"turn_id on session.end", `{"type":"event","name":"session.end","turn_id":"t-1"}`},
		{"turn_id on an observe event", `{"type":"event","name":"effort","level":"high","turn_id":"t-1"}`},
		{"non-string turn_id", `{"type":"event","name":"turn.start","turn_id":7}`},
		{"null command_id", `{"type":"event","name":"turn.start","command_id":null}`},
		{"control character in turn_id", "{\"type\":\"event\",\"name\":\"turn.start\",\"turn_id\":\"a\\nb\"}"},
		{"oversized turn_id", `{"type":"event","name":"turn.start","turn_id":"` + strings.Repeat("a", MaxEventIDLen+1) + `"}`},
		{"oversized command_id", `{"type":"event","name":"turn.start","command_id":"` + strings.Repeat("a", MaxEventIDLen+1) + `"}`},
		{"origin with spaces", `{"type":"event","name":"turn.start","origin":"a b"}`},
		{"oversized origin", `{"type":"event","name":"turn.start","origin":"` + strings.Repeat("a", 33) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseReport([]byte(tc.body)); !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("err = %v, want ErrInvalidReport", err)
			}
		})
	}
}

func TestHookPayloadCarriesTurnIdentity(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want map[string]any
	}{
		{
			name: "turn.start names its turn, command and origin",
			ev:   Event{Name: EventTurnStart, SessionID: "s-1", TurnID: "t-1", CommandID: "c-9", Origin: "plugin"},
			want: map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s-1", "bridge_turn_id": "t-1", "command_id": "c-9", "origin": "plugin"},
		},
		{
			name: "an unstamped turn.start carries only its turn",
			ev:   Event{Name: EventTurnStart, TurnID: "t-2"},
			want: map[string]any{"hook_event_name": "UserPromptSubmit", "bridge_turn_id": "t-2"},
		},
		{
			name: "turn.complete names the turn it ends",
			ev:   Event{Name: EventTurnComplete, TurnID: "t-1", Message: "done"},
			want: map[string]any{"hook_event_name": "Stop", "bridge_turn_id": "t-1", "last_assistant_message": "done"},
		},
		{
			name: "an aborted turn keeps its turn id",
			ev:   Event{Name: EventTurnComplete, TurnID: "t-1", Reason: TurnAborted},
			want: map[string]any{"hook_event_name": "Interrupt", "bridge_turn_id": "t-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, raw, ok := HookPayload(tc.ev)
			if !ok {
				t.Fatal("no payload")
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubscriberSeesTurnIdentity(t *testing.T) {
	h := newTestHub(newFakeClock())
	var got []Event
	h.AddSubscriber(SubscriberFunc(func(ev Event) { got = append(got, ev) }))
	apply(t, h, agentA, hello("s-1"))
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnStart, TurnID: "t-1", CommandID: "c-9", Origin: "plugin"})
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnComplete, TurnID: "t-1"})

	if len(got) != 3 {
		t.Fatalf("subscriber saw %d events, want 3", len(got))
	}
	if start := got[1]; start.TurnID != "t-1" || start.CommandID != "c-9" || start.Origin != "plugin" {
		t.Fatalf("turn.start identity lost: %+v", start)
	}
	if done := got[2]; done.TurnID != "t-1" {
		t.Fatalf("turn.complete turn id lost: %+v", done)
	}
}
