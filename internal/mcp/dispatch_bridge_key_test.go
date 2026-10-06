package mcp

import (
	"encoding/json"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// A bridged claude's MCP server inherits its LEO_BRIDGE_AGENT; the dispatch
// names it so the daemon can route the dispatch's state to that claude's mod.
func TestLeoDispatchSendsTheCallersBridgeKey(t *testing.T) {
	for _, tc := range []struct{ env, want string }{{"alpha", "alpha"}, {"", ""}} {
		t.Setenv("LEO_BRIDGE_AGENT", tc.env)
		var gotBody map[string]any
		d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
			_ = json.Unmarshal(body, &gotBody)
			return 200, `{"ok":true,"data":{"id":"d-test","harness":"codex","model":"gpt","cwd":"/tmp"}}`
		})
		reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant", leotools.Permissions{})
		runRequest(t, reg, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "leo_dispatch", "arguments": map[string]any{"template": "codex", "prompt": "do it", "cwd": "/tmp"}}})
		d.close()
		got, present := gotBody["caller_bridge_key"]
		if tc.want == "" {
			if present {
				t.Fatalf("no bridge key in env, but sent %v", got)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("caller_bridge_key = %v, want %q (body %v)", got, tc.want, gotBody)
		}
	}
}
