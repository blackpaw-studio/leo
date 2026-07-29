package claude

import (
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
		{
			name: "init is not worth showing",
			line: `{"type":"system","subtype":"init","session_id":"s1"}`,
		},
		{
			name: "assistant text",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the dispatcher."}]}}`,
			want: []harness.Event{{Kind: harness.EventText, Summary: "Looking at the dispatcher."}},
		},
		{
			name: "tool call summarized by its salient input",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"internal/consult/consult.go","offset":1}}]}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "Read", Summary: "internal/consult/consult.go"}},
		},
		{
			name: "bash call shows the command, collapsed to one line",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...\ngo vet ./...","description":"run tests"}}]}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "Bash", Summary: "go test ./..."}},
		},
		{
			name: "unknown tool input renders a bare tool name",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Mystery","input":{"count":3}}]}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "Mystery"}},
		},
		{
			name: "one line can carry text and a tool call",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"Now the tests."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			want: []harness.Event{
				{Kind: harness.EventText, Summary: "Now the tests."},
				{Kind: harness.EventTool, Tool: "Bash", Summary: "go test ./..."},
			},
		},
		{
			name: "successful tool results stay out of the feed",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"// Package consult…"}]}}`,
		},
		{
			name: "failed tool results surface",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"File does not exist.\nnope"}]}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "File does not exist."}},
		},
		{
			name: "tool result content sent as blocks",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"boom"}]}]}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "boom"}},
		},
		{
			name: "final result",
			line: `{"type":"result","subtype":"success","result":"The dispatcher discards the stream.","is_error":false}`,
			want: []harness.Event{{Kind: harness.EventResult, Summary: "The dispatcher discards the stream."}},
		},
		{
			name: "failed result",
			line: `{"type":"result","is_error":true,"errors":["max turns exceeded"]}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "max turns exceeded"}},
		},
		{name: "non-JSON", line: "not json at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Claude{}.RenderEvent([]byte(tt.line))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RenderEvent = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestClaudeImplementsEventRenderer locks in the capability assertion the
// CLI makes.
func TestClaudeImplementsEventRenderer(t *testing.T) {
	var h any = Claude{}
	if _, ok := h.(harness.EventRenderer); !ok {
		t.Fatal("Claude does not implement harness.EventRenderer")
	}
}
