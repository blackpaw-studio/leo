package bridge

import (
	"errors"
	"testing"
)

func TestParseReportTurnTokens(t *testing.T) {
	body := `{"type":"event","name":"turn.complete","event_id":"turn.complete:t1",` +
		`"tokens":{"input":120,"output":45,"cache_read":9000,"cache_creation":300,"model":"claude-opus-5-5"}}`
	got, err := ParseReport([]byte(body))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	want := TurnTokens{Input: 120, Output: 45, CacheRead: 9000, CacheCreation: 300, Model: "claude-opus-5-5"}
	if got.Tokens == nil || *got.Tokens != want {
		t.Fatalf("Tokens = %+v, want %+v", got.Tokens, want)
	}
}

// A turn the engine counted nothing for still reports its tokens: zeros,
// and no model.
func TestParseReportZeroTurnTokens(t *testing.T) {
	got, err := ParseReport([]byte(`{"type":"event","name":"turn.complete","tokens":{"input":0,"output":0,"cache_read":0,"cache_creation":0}}`))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.Tokens == nil || *got.Tokens != (TurnTokens{}) {
		t.Fatalf("Tokens = %+v, want zeros", got.Tokens)
	}
}

func TestParseReportWithoutTokens(t *testing.T) {
	got, err := ParseReport([]byte(`{"type":"event","name":"turn.complete","usage":{"u":1}}`))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.Tokens != nil {
		t.Fatalf("Tokens = %+v, want nil when the mod sent none", got.Tokens)
	}
}

func TestParseReportRejectsBadTokens(t *testing.T) {
	cases := map[string]string{
		"on turn.start":     `{"type":"event","name":"turn.start","tokens":{"input":1,"output":1,"cache_read":0,"cache_creation":0}}`,
		"on session.end":    `{"type":"event","name":"session.end","tokens":{"input":1,"output":1,"cache_read":0,"cache_creation":0}}`,
		"not an object":     `{"type":"event","name":"turn.complete","tokens":[1,2]}`,
		"null":              `{"type":"event","name":"turn.complete","tokens":null}`,
		"negative count":    `{"type":"event","name":"turn.complete","tokens":{"input":-1,"output":0,"cache_read":0,"cache_creation":0}}`,
		"fractional count":  `{"type":"event","name":"turn.complete","tokens":{"input":1.5,"output":0,"cache_read":0,"cache_creation":0}}`,
		"missing count":     `{"type":"event","name":"turn.complete","tokens":{"input":1,"output":0,"cache_read":0}}`,
		"count wrong kind":  `{"type":"event","name":"turn.complete","tokens":{"input":"1","output":0,"cache_read":0,"cache_creation":0}}`,
		"unknown key":       `{"type":"event","name":"turn.complete","tokens":{"input":1,"output":0,"cache_read":0,"cache_creation":0,"cost":1}}`,
		"model wrong kind":  `{"type":"event","name":"turn.complete","tokens":{"input":1,"output":0,"cache_read":0,"cache_creation":0,"model":7}}`,
		"tokens on an ack":  `{"type":"ack","id":"c1","ok":true,"tokens":{}}`,
		"tokens on a hello": `{"type":"hello","session_id":"s","claude_version":"v","tokens":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReport([]byte(body)); !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("ParseReport(%s) err = %v, want ErrInvalidReport", body, err)
			}
		})
	}
}

// A subscriber sees the turn's tokens on its turn.complete event.
func TestHubEventCarriesTurnTokens(t *testing.T) {
	var got []Event
	h := New(Options{Subscriber: SubscriberFunc(func(ev Event) { got = append(got, ev) })})
	t.Cleanup(h.Close)
	if _, err := h.Open("worker", "launch-1"); err != nil {
		t.Fatal(err)
	}
	tokens := &TurnTokens{Input: 3, Output: 4, CacheRead: 5, CacheCreation: 6, Model: "m"}
	if err := h.Apply("worker", "launch-1", Report{Type: ReportEvent, Name: EventTurnComplete, Tokens: tokens}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 1 || got[0].Tokens == nil || *got[0].Tokens != *tokens {
		t.Fatalf("events = %+v, want one turn.complete carrying %+v", got, *tokens)
	}
	if got[0].Tokens == tokens {
		t.Fatal("event shares the report's tokens; want a copy")
	}
}
