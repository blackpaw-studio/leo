package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestParseEventsFixtures(t *testing.T) {
	tests := []struct {
		file string
		want harness.Result
	}{
		{"fresh.jsonl", harness.Result{SessionID: "019f4eba-a1a6-77b0-be48-091cd08350e9", Text: "pong"}},
		{"resume.jsonl", harness.Result{SessionID: "019f4eba-a1a6-77b0-be48-091cd08350e9", Text: "pong"}},
		{"badmodel.jsonl", harness.Result{
			SessionID: "019f4ebb-41e4-7b61-a83c-ba50db2be2cd",
			IsError:   true,
			// item error + top-level error + turn.failed, in stream order
			Errors: []string{
				"Model metadata for `not-a-real-model` not found. Defaulting to fallback metadata; this can degrade performance and cause issues.",
				"{\n  \"type\": \"error\",\n  \"error\": {\n    \"type\": \"invalid_request_error\",\n    \"code\": \"model_not_found\",\n    \"message\": \"The requested model 'not-a-real-model' does not exist.\",\n    \"param\": \"model\"\n  },\n  \"status\": 400\n}",
				"{\n  \"type\": \"error\",\n  \"error\": {\n    \"type\": \"invalid_request_error\",\n    \"code\": \"model_not_found\",\n    \"message\": \"The requested model 'not-a-real-model' does not exist.\",\n    \"param\": \"model\"\n  },\n  \"status\": 400\n}",
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := Codex{}.ParseEvents(f)
			if err != nil {
				t.Fatalf("ParseEvents: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseEventsMCPToolCall(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "mcp_tool_call.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Codex{}.ParseEvents(f)
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{SessionID: "019f4ebe-c9ad-71c2-b77c-851c3102efed", Text: "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseEventsMCPCancelled(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "mcp_cancelled.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Codex{}.ParseEvents(f)
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{
		SessionID: "019f4ebb-d318-7fc1-bb09-229d3b45157f",
		Text:      "0",
		IsError:   false,
		Errors:    []string{"user cancelled MCP tool call", "user cancelled MCP tool call"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseEventsEmptyStream(t *testing.T) {
	got, err := Codex{}.ParseEvents(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	if !reflect.DeepEqual(got, harness.Result{}) {
		t.Errorf("got %+v, want zero Result", got)
	}
}

func TestParseEventsGarbageLinesSkipped(t *testing.T) {
	input := "not json\n{\"type\":\"thread.started\",\"thread_id\":\"abc\"}\n{{{garbage\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"hi\"}}\n"
	got, err := Codex{}.ParseEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{SessionID: "abc", Text: "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
