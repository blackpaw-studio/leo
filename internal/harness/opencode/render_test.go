package opencode

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestRenderEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []harness.Event
	}{
		{name: "step start is bookkeeping", line: `{"type":"step_start","part":{"type":"step-start"}}`},
		{name: "step finish is bookkeeping", line: `{"type":"step_finish","part":{"reason":"stop"}}`},
		{
			name: "text",
			line: `{"type":"text","sessionID":"s1","part":{"type":"text","text":"pong"}}`,
			want: []harness.Event{{Kind: harness.EventText, Summary: "pong"}},
		},
		{
			name: "tool use carries the call's title",
			line: `{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"status":"completed","title":"internal/consult/consult.go"}}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "read", Summary: "internal/consult/consult.go"}},
		},
		{
			name: "error prefers the nested message",
			line: `{"type":"error","error":{"name":"ProviderError","data":{"message":"model not found"}}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "model not found"}},
		},
		{
			name: "error falls back to the name",
			line: `{"type":"error","error":{"name":"UnknownError","data":{}}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "UnknownError"}},
		},
		{name: "non-JSON log noise", line: "ERROR  service=default unhandled error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Opencode{}.RenderEvent([]byte(tt.line))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RenderEvent = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestRenderEventOverCapturedStreams runs the renderer over real captured
// runs, so the shapes stay tied to what opencode actually emits.
func TestRenderEventOverCapturedStreams(t *testing.T) {
	tests := []struct {
		fixture string
		want    []harness.Event
	}{
		{"fresh.jsonl", []harness.Event{{Kind: harness.EventText, Summary: "pong"}}},
		{"multistep_deny.jsonl", []harness.Event{
			{Kind: harness.EventTool, Tool: "invalid", Summary: "Invalid Tool"},
			{Kind: harness.EventText, Summary: "BLOCKED"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatalf("opening fixture: %v", err)
			}
			defer f.Close()

			var got []harness.Event
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for scanner.Scan() {
				got = append(got, Opencode{}.RenderEvent(scanner.Bytes())...)
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scanning fixture: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("feed = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestOpencodeImplementsEventRenderer(t *testing.T) {
	var h any = Opencode{}
	if _, ok := h.(harness.EventRenderer); !ok {
		t.Fatal("Opencode does not implement harness.EventRenderer")
	}
}
