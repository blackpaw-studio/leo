package mcp

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// A caller that misspells a parameter must be told, not served a default.
func TestToolCallRejectsUnknownArgument(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"messages":[]}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})

	out := reg.callTool(t.Context(), "leo_chat_history", json.RawMessage(`{"thread":"openrct2-zoo"}`))
	if !out.IsError {
		t.Fatalf("unknown parameter was accepted: %+v", out)
	}
	msg := outcomeText(t, out)
	for _, want := range []string{"leo_chat_history", `"thread"`, "accepted:", "limit", "before", "task"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("daemon was called despite the bad argument: %v", d.calls)
	}
}

func TestToolCallWithKnownArgumentsStillWorks(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"messages":[]}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})

	out := reg.callTool(t.Context(), "leo_chat_history", json.RawMessage(`{"limit":5,"task":"daily"}`))
	if out.IsError {
		t.Fatalf("valid call failed: %s", outcomeText(t, out))
	}
}

// Every registered tool, including ones added later, rejects an unknown key.
func TestEveryRegisteredToolRejectsUnknownArgument(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	if len(reg.defs) == 0 {
		t.Fatal("no tools registered")
	}
	for _, def := range reg.defs {
		t.Run(def.Name, func(t *testing.T) {
			out := reg.callTool(t.Context(), def.Name, json.RawMessage(`{"definitely_not_a_param":1}`))
			if !out.IsError || !strings.Contains(outcomeText(t, out), `unknown parameter "definitely_not_a_param"`) {
				t.Fatalf("not rejected: isError=%v %q", out.IsError, outcomeText(t, out))
			}
			if def.InputSchema["additionalProperties"] != false {
				t.Errorf("schema does not advertise additionalProperties:false")
			}
			props, _ := def.InputSchema["properties"].(map[string]any)
			names := make([]string, 0, len(props))
			for k := range props {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) > 0 && !strings.Contains(outcomeText(t, out), strings.Join(names, ", ")) {
				t.Errorf("error %q does not list accepted %v", outcomeText(t, out), names)
			}
		})
	}
}

// The fixed app-call tools are Leo's own schema, so they are checked too; the
// tool's `args` object is the app's and passes through.
func TestAppCallFallbackRejectsUnknownArgument(t *testing.T) {
	_, client := newClientToolsDaemon(t)
	reg := newRegistry(client, "alfred", leotools.Permissions{})
	reg.setClientName("codex-mcp-client")

	out := reg.callTool(t.Context(), "leo_app_call", json.RawMessage(`{"tool":"browser_click","arg":{"ref":"e1"}}`))
	if got := outcomeText(t, out); !out.IsError || !strings.Contains(got, `unknown parameter "arg" (accepted: args, tool)`) {
		t.Fatalf("outcome = %q (error=%v)", got, out.IsError)
	}
}
