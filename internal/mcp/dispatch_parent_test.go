package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// A leo_dispatch subagent's MCP server knows the dispatch it serves
// (LEO_DISPATCH_ID); the new dispatch names it as its parent, headless
// parents included.
func TestLeoDispatchNamesTheCallingDispatchAsParent(t *testing.T) {
	for _, tc := range []struct{ dispatchID, want string }{{"d-self", "d-self"}, {"", ""}} {
		var gotBody map[string]any
		d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
			_ = json.Unmarshal(body, &gotBody)
			return 200, `{"ok":true,"data":{"id":"d-test","harness":"codex","model":"gpt","cwd":"/tmp"}}`
		})
		reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant", leotools.Permissions{}, withDispatchID(tc.dispatchID))
		runRequest(t, reg, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "leo_dispatch", "arguments": map[string]any{"template": "codex", "prompt": "do it", "cwd": "/tmp"}}})
		d.close()
		// The calling dispatch's own process name names no agent, so it is
		// the parent id, not "from", that attributes the new dispatch.
		wantFrom := "assistant"
		if tc.dispatchID != "" {
			wantFrom = ""
		}
		if gotBody["from"] != wantFrom {
			t.Fatalf("from = %v, want %q (body %v)", gotBody["from"], wantFrom, gotBody)
		}
		got, present := gotBody["parent_dispatch_id"]
		if tc.want == "" {
			if present {
				t.Fatalf("not inside a dispatch, but sent parent_dispatch_id %v", got)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("parent_dispatch_id = %v, want %q (body %v)", got, tc.want, gotBody)
		}
	}
}

// leo_consult inside a dispatch names that dispatch (so the daemon parents the
// consult under it) and, like leo_dispatch, not its synthetic process name.
func TestLeoConsultNamesTheCallingDispatch(t *testing.T) {
	for _, tc := range []struct{ dispatchID, wantParent, wantFrom string }{{"d-self", "d-self", ""}, {"", "", "assistant"}} {
		var got map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got)
			_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"c-1","harness":"claude","model":"m","text":"hi"}}`))
		}))
		reg := newRegistry(newDaemonClient(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), "tok"), "assistant", leotools.Permissions{}, withDispatchID(tc.dispatchID))
		if _, err := callTool(reg, "leo_consult", map[string]any{"template": "claude", "prompt": "q"}); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if got["parent_dispatch_id"] != tc.wantParent || got["from"] != tc.wantFrom {
			t.Fatalf("dispatchID %q: parent=%q from=%q, want %q / %q", tc.dispatchID, got["parent_dispatch_id"], got["from"], tc.wantParent, tc.wantFrom)
		}
	}
}
