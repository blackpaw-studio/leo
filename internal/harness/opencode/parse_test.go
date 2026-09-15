package opencode

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
		{"fresh.jsonl", harness.Result{SessionID: "ses_0b15975f3ffeiBHJ9UNhtqbZzJ", Text: "pong"}},
		{"resume.jsonl", harness.Result{SessionID: "ses_0b15975f3ffeiBHJ9UNhtqbZzJ", Text: "pong"}},
		// EOF is end-of-turn; the final step_finish is never required.
		{"truncated_no_step_finish.jsonl", harness.Result{SessionID: "ses_0b15975f3ffeiBHJ9UNhtqbZzJ", Text: "pong"}},
		{"multistep_deny.jsonl", harness.Result{SessionID: "ses_0b157fc32ffeIbZKrVjBbNRpSx", Text: "BLOCKED"}},
		{"badmodel.jsonl", harness.Result{
			SessionID: "ses_0b1589bc3ffetZTI1wwEehWuBB",
			IsError:   true,
			Errors: []string{
				"Unexpected server error. Check server logs for details.",
				"Model not found: anthropic/not-a-real-model.",
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
			got, err := Opencode{}.ParseEvents(f)
			if err != nil {
				t.Fatalf("ParseEvents: %v", err)
			}
			if got.Usage == nil && (tt.file == "fresh.jsonl" || tt.file == "resume.jsonl" || tt.file == "multistep_deny.jsonl") {
				t.Fatalf("fixture %s has no usage", tt.file)
			}
			outcome := got
			outcome.Usage = nil
			if !reflect.DeepEqual(outcome, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseEventsEmptyStream(t *testing.T) {
	got, err := Opencode{}.ParseEvents(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	if !reflect.DeepEqual(got, harness.Result{}) {
		t.Errorf("got %+v, want zero Result", got)
	}
}

func TestParseEventsMultiText(t *testing.T) {
	input := `{"type":"text","sessionID":"ses_x","part":{"text":"one"}}
{"type":"text","sessionID":"ses_x","part":{"text":"two"}}
`
	got, err := Opencode{}.ParseEvents(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	want := harness.Result{SessionID: "ses_x", Text: "one\ntwo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseEventsUsageIncludesReasoningAndDeduplicatesParts(t *testing.T) {
	stream := `{"type":"step_start","part":{"id":"s1"}}
{"type":"step_start","part":{"id":"s1"}}
{"type":"tool_use","part":{"id":"t1","type":"tool"}}
{"type":"tool_use","part":{"id":"t1","type":"tool"}}
{"type":"step_finish","part":{"id":"finish1","tokens":{"input":10,"output":2,"reasoning":3}}}
{"type":"step_finish","part":{"id":"finish1","tokens":{"input":10,"output":2,"reasoning":3}}}`
	got, err := Opencode{}.ParseEvents(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || *got.Usage.InputTokens != 10 || *got.Usage.OutputTokens != 5 || *got.Usage.Turns != 1 || *got.Usage.ToolCalls != 1 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

func TestParseEventsFixtureUsageIncludesCacheInputs(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "fresh.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Opencode{}.ParseEvents(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || *got.Usage.InputTokens != 15965 || *got.Usage.OutputTokens != 17 || *got.Usage.Turns != 1 || *got.Usage.ToolCalls != 0 {
		t.Fatalf("usage=%#v", got.Usage)
	}
}

func TestParseEventsCountsOnlyExactToolUseShape(t *testing.T) {
	stream := `{"type":"tool_result","part":{"id":"wrong","type":"tool-result"}}
{"type":"not-a-tool","part":{"id":"also-wrong","type":"tool"}}
{"type":"tool_use","part":{"id":"right","type":"tool"}}`
	got, err := Opencode{}.ParseEvents(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || got.Usage.ToolCalls == nil || *got.Usage.ToolCalls != 1 {
		t.Fatalf("usage=%#v", got.Usage)
	}
}

func TestParseEventsTruncatedFixtureLeavesTokensUnknown(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "truncated_no_step_finish.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Opencode{}.ParseEvents(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || got.Usage.InputTokens != nil || got.Usage.OutputTokens != nil || got.Usage.Turns == nil || *got.Usage.Turns != 1 || got.Usage.ToolCalls == nil || *got.Usage.ToolCalls != 0 {
		t.Fatalf("usage=%#v", got.Usage)
	}
}
