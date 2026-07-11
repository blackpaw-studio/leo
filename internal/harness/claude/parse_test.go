package claude

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// TestParseEvents ports the characterization coverage from the pre-move
// internal/run.parseClaudeOutput tests verbatim, re-expressed against the
// ParseEvents API.
func TestParseEvents(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantSID  string
		wantText string
	}{
		{
			name:     "stream-json NDJSON",
			output:   "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"abc-123\"}\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Hi\"}]}}\n{\"type\":\"result\",\"session_id\":\"abc-123\",\"result\":\"Hello world\",\"is_error\":false}\n",
			wantSID:  "abc-123",
			wantText: "Hello world",
		},
		{
			name:     "stream-json error",
			output:   "{\"type\":\"result\",\"session_id\":\"def-456\",\"result\":\"failed\",\"is_error\":true}\n",
			wantSID:  "def-456",
			wantText: "failed",
		},
		{
			name:     "fallback single JSON object",
			output:   `{"session_id":"abc-123","result":"Hello world","is_error":false}`,
			wantSID:  "abc-123",
			wantText: "Hello world",
		},
		{
			name:    "invalid JSON",
			output:  "not json at all",
			wantSID: "",
		},
		{
			name:    "empty",
			output:  "",
			wantSID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Claude{}.ParseEvents(strings.NewReader(tt.output))
			if err != nil {
				t.Fatalf("ParseEvents: %v", err)
			}
			if result.SessionID != tt.wantSID {
				t.Errorf("SessionID = %q, want %q", result.SessionID, tt.wantSID)
			}
			if result.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", result.Text, tt.wantText)
			}
		})
	}
}

func TestParseEventsStreamJSON(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}
{"type":"result","session_id":"abc-123","result":"done","is_error":false}
`
	res, err := Claude{}.ParseEvents(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{SessionID: "abc-123", Text: "done"}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
}

func TestParseEventsSingleObjectFallback(t *testing.T) {
	res, _ := Claude{}.ParseEvents(strings.NewReader(`{"session_id":"s1","result":"ok"}`))
	if res.SessionID != "s1" || res.Text != "ok" {
		t.Errorf("fallback parse got %+v", res)
	}
}

func TestParseEventsErrors(t *testing.T) {
	stream := `{"type":"result","session_id":"s2","is_error":true,"errors":["boom"]}`
	res, _ := Claude{}.ParseEvents(strings.NewReader(stream))
	if !res.IsError || len(res.Errors) != 1 || res.Errors[0] != "boom" {
		t.Errorf("got %+v", res)
	}
}
