package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMessageJSONAlwaysCarriesStateAndAttachments(t *testing.T) {
	m := Message{ID: "01X", Agent: "assistant", Direction: DirectionOut, Kind: KindText, Text: "hi", TS: time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"state":""`, `"attachments":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	// Pointers marshal the same (events carry *Payload with a Message).
	b, _ = json.Marshal(&MessagePayload{Agent: "assistant", Message: m})
	if !strings.Contains(string(b), `"attachments":[]`) {
		t.Errorf("event payload %s lacks attachments", b)
	}
}

func TestMessageJSONOmitsAnEmptyDeadlineAndReason(t *testing.T) {
	m := Message{ID: "01X", Agent: "assistant", Kind: KindAsk, State: StateOpen}
	b, _ := json.Marshal(m)
	for _, absent := range []string{`"deadline"`, `"closed_reason"`, `"Asker"`, `"asker"`} {
		if strings.Contains(string(b), absent) {
			t.Errorf("%s carries %s", b, absent)
		}
	}
	m.Deadline = time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	m.ClosedReason = ClosedWithdrawn
	b, _ = json.Marshal(m)
	for _, want := range []string{`"deadline":"2026-10-15T18:00:00Z"`, `"closed_reason":"withdrawn"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
}
