package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// leo_dispatch forwards additional_mcp_servers only when the caller gave a
// non-empty list, so a new tool talking to an older daemon is unaffected.
func TestLeoDispatchForwardsAdditionalMCPServers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		arg    any
		want   any
		wantIn bool
	}{
		{"omitted is absent", nil, nil, false},
		{"empty is absent", []any{}, nil, false},
		{"named is sent", []any{"chrome", "other"}, []any{"chrome", "other"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
				_ = json.Unmarshal(body, &gotBody)
				return 200, `{"ok":true,"data":{"id":"d-test","harness":"codex","model":"gpt","cwd":"/tmp"}}`
			})
			defer d.close()
			reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant", leotools.Permissions{})
			args := map[string]any{"template": "codex", "prompt": "do it", "cwd": "/tmp"}
			if tc.arg != nil {
				args["additional_mcp_servers"] = tc.arg
			}
			if _, err := callTool(reg, "leo_dispatch", args); err != nil {
				t.Fatal(err)
			}
			got, in := gotBody["additional_mcp_servers"]
			if in != tc.wantIn || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("additional_mcp_servers = %#v (present=%v), want %#v (present=%v)", got, in, tc.want, tc.wantIn)
			}
		})
	}
}
