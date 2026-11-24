package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// leo_dispatch forwards target_agent and, with it, no cwd: the daemon runs
// the dispatch in the target's workspace, so the server's own cwd must not
// be filled in.
func TestLeoDispatchForwardsTargetAgentWithoutCwd(t *testing.T) {
	var gotBody map[string]any
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		if strings.Contains(path, "dispatch") {
			_ = json.Unmarshal(body, &gotBody)
		}
		return 200, `{"ok":true,"data":{"id":"d-test","harness":"claude","model":"opus","cwd":"/ws/leo","template":"scout"}}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), "tok"), "marketing", leotools.Permissions{})
	if _, err := callTool(reg, "leo_dispatch", map[string]any{"role": "explore", "prompt": "how does X work", "target_agent": "leo"}); err != nil {
		t.Fatal(err)
	}
	if gotBody["target_agent"] != "leo" {
		t.Fatalf("body = %#v, want target_agent leo", gotBody)
	}
	if cwd, set := gotBody["cwd"]; set {
		t.Fatalf("body carries cwd %v alongside target_agent", cwd)
	}
	// Without target_agent the server's cwd is still the default.
	if _, err := callTool(reg, "leo_dispatch", map[string]any{"role": "explore", "prompt": "look"}); err != nil {
		t.Fatal(err)
	}
	if cwd, _ := gotBody["cwd"].(string); cwd == "" {
		t.Fatalf("plain dispatch lost its default cwd: %#v", gotBody)
	}
	tools := runRequest(t, reg, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
	if !strings.Contains(fmt.Sprint(tools), "target_agent") {
		t.Fatal("leo_dispatch schema does not advertise target_agent")
	}
}
