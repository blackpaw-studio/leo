package codex

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
		{name: "thread start is bookkeeping", line: `{"type":"thread.started","thread_id":"t1"}`},
		{name: "turn start is bookkeeping", line: `{"type":"turn.started"}`},
		{
			name: "command execution renders when it starts",
			line: `{"type":"item.started","item":{"id":"i0","type":"command_execution","command":"go test ./..."}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "bash", Summary: "go test ./..."}},
		},
		{
			name: "mcp tool call is qualified by its server",
			line: `{"type":"item.started","item":{"id":"i0","type":"mcp_tool_call","server":"leo","tool":"leo_list_agents"}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "leo/leo_list_agents"}},
		},
		{
			name: "unknown item types degrade to their type name",
			line: `{"type":"item.started","item":{"id":"i0","type":"web_search"}}`,
			want: []harness.Event{{Kind: harness.EventTool, Tool: "web_search"}},
		},
		{name: "reasoning is internal", line: `{"type":"item.started","item":{"id":"i0","type":"reasoning"}}`},
		{
			name: "agent message renders on completion",
			line: `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"2"}}`,
			want: []harness.Event{{Kind: harness.EventText, Summary: "2"}},
		},
		{
			name: "completed tool calls are silent unless they failed",
			line: `{"type":"item.completed","item":{"id":"i0","type":"mcp_tool_call","server":"leo","tool":"x","error":null}}`,
		},
		{
			name: "failed tool call surfaces its error",
			line: `{"type":"item.completed","item":{"id":"i0","type":"mcp_tool_call","error":{"message":"connection refused"}}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "connection refused"}},
		},
		{
			name: "a failed command reports through its exit code, not item.error",
			line: `{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"go build ./...","exit_code":2,"aggregated_output":"consult.go:12: undefined: foo\nmore"}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "consult.go:12: undefined: foo"}},
		},
		{
			name: "a failed command with no captured output still reports",
			line: `{"type":"item.completed","item":{"id":"i0","type":"command_execution","exit_code":127}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "command exited 127"}},
		},
		{
			name: "a successful command stays silent",
			line: `{"type":"item.completed","item":{"id":"i0","type":"command_execution","exit_code":0}}`,
		},
		{
			name: "an absent exit code is not read as success or failure",
			line: `{"type":"item.completed","item":{"id":"i0","type":"command_execution"}}`,
		},
		{
			name: "turn failure",
			line: `{"type":"turn.failed","error":{"message":"model overloaded"}}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "model overloaded"}},
		},
		{
			name: "top-level error",
			line: `{"type":"error","message":"bad model"}`,
			want: []harness.Event{{Kind: harness.EventError, Summary: "bad model"}},
		},
		{name: "non-JSON", line: "{{{garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Codex{}.RenderEvent([]byte(tt.line))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RenderEvent = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestRenderEventOverCapturedStream runs the renderer over a real captured
// run, so the shapes stay tied to what codex actually emits.
func TestRenderEventOverCapturedStream(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "mcp_tool_call.jsonl"))
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()

	var got []harness.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		got = append(got, Codex{}.RenderEvent(scanner.Bytes())...)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}

	want := []harness.Event{
		{Kind: harness.EventTool, Tool: "leo/leo_list_agents"},
		{Kind: harness.EventText, Summary: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("feed = %+v, want %+v", got, want)
	}
}

func TestCodexImplementsEventRenderer(t *testing.T) {
	var h any = Codex{}
	if _, ok := h.(harness.EventRenderer); !ok {
		t.Fatal("Codex does not implement harness.EventRenderer")
	}
}
