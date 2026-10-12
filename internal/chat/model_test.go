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
