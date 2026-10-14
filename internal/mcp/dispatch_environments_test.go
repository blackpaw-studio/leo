package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// leo_dispatch forwards an environments override as given, keeping an empty
// list (inherit nothing) distinct from an omitted one (inherit).
func TestLeoDispatchForwardsEnvironments(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  any
		want any
	}{
		{"omitted inherits", nil, nil},
		{"named override", []any{"work"}, []any{"work"}},
		{"empty inherits nothing", []any{}, []any{}},
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
				args["environments"] = tc.arg
			}
			if _, err := callTool(reg, "leo_dispatch", args); err != nil {
				t.Fatal(err)
			}
			if got := gotBody["environments"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("environments = %#v, want %#v", got, tc.want)
			}
		})
	}
}
