package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

func surfaceDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "POST" && strings.HasSuffix(path, "/surface-file") {
			return 200, `{"ok":true,"data":{"id":"11111111-2222-4333-8444-555555555555"}}`
		}
		return 404, `{"ok":false,"error":"unexpected"}`
	})
	t.Cleanup(d.close)
	return d
}

func TestSurfaceFileSchema(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "primary", leotools.Permissions{})
	var def *toolDef
	for i := range reg.defs {
		if reg.defs[i].Name == "leo_surface_file" {
			def = &reg.defs[i]
		}
	}
	if def == nil {
		t.Fatal("leo_surface_file is not registered")
	}
	props := def.InputSchema["properties"].(map[string]any)
	if props["path"].(map[string]any)["type"] != "string" ||
		props["line"].(map[string]any)["type"] != "integer" ||
		props["line"].(map[string]any)["minimum"] != 1 ||
		props["reason"].(map[string]any)["type"] != "string" ||
		props["reason"].(map[string]any)["maxLength"] != 200 {
		t.Fatalf("schema properties = %+v", props)
	}
	if req := def.InputSchema["required"].([]string); !slices.Equal(req, []string{"path"}) {
		t.Fatalf("required = %v, want [path]", req)
	}
}

func TestSurfaceFilePostsToDaemonAndReturnsID(t *testing.T) {
	d := surfaceDaemon(t)
	reg := newRegistry(newDaemonClient(d.port(), ""), "my agent", leotools.Permissions{})

	out, err := reg.call("leo_surface_file", json.RawMessage(`{"path":"dir/ü x.go","line":7,"reason":"look here"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != `{"id":"11111111-2222-4333-8444-555555555555"}` {
		t.Fatalf("result = %s", out)
	}
	if len(d.calls) != 1 || d.calls[0].Path != "/api/agent/my agent/surface-file" {
		t.Fatalf("calls = %+v", d.calls)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(d.calls[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["path"] != "dir/ü x.go" || body["line"] != float64(7) || body["reason"] != "look here" {
		t.Fatalf("body = %s", d.calls[0].Body)
	}
	if _, ok := body["dispatch_id"]; ok {
		t.Fatalf("agent call must not carry a dispatch id: %s", d.calls[0].Body)
	}
}

func TestSurfaceFileOmitsUnsetOptionalFields(t *testing.T) {
	d := surfaceDaemon(t)
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})

	if _, err := reg.call("leo_surface_file", json.RawMessage(`{"path":"/abs/a.go"}`)); err != nil {
		t.Fatal(err)
	}
	if got := d.calls[0].Body; got != `{"path":"/abs/a.go"}` {
		t.Fatalf("body = %s", got)
	}
}

func TestSurfaceFileArgumentRejections(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"missing path", `{}`},
		{"empty path", `{"path":""}`},
		{"path not a string", `{"path":3}`},
		{"line zero", `{"path":"a","line":0}`},
		{"line fractional", `{"path":"a","line":2.5}`},
		{"line string", `{"path":"a","line":"3"}`},
		{"reason not a string", `{"path":"a","reason":5}`},
		{"reason 201 chars", `{"path":"a","reason":"` + strings.Repeat("ü", 201) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := surfaceDaemon(t)
			reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})

			if _, err := reg.call("leo_surface_file", json.RawMessage(tc.args)); err == nil {
				t.Fatal("expected an error")
			}
			if len(d.calls) != 0 {
				t.Fatalf("invalid call reached the daemon: %+v", d.calls)
			}
		})
	}
}

func TestSurfaceFileReason200CharsAccepted(t *testing.T) {
	d := surfaceDaemon(t)
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := reg.call("leo_surface_file", json.RawMessage(`{"path":"a","reason":"`+strings.Repeat("ü", 200)+`"}`)); err != nil {
		t.Fatalf("200-char reason rejected: %v", err)
	}
}

func TestSurfaceFileDaemonErrorIsReturned(t *testing.T) {
	d := newFakeDaemon(func(string, string, []byte) (int, string) {
		return 400, `{"ok":false,"error":"file not found: /w/a"}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})

	_, err := reg.call("leo_surface_file", json.RawMessage(`{"path":"a"}`))
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("err = %v", err)
	}
}

// TestSurfaceFileRejectsDispatchSubagentFromEnv drives the real env path: a
// dispatched subagent inherits its caller's LEO_PROCESS_NAME, so only
// LEO_DISPATCH_ID tells it apart.
func TestSurfaceFileRejectsDispatchSubagentFromEnv(t *testing.T) {
	d := surfaceDaemon(t)
	t.Setenv("LEO_PROCESS_NAME", "primary")
	t.Setenv("LEO_WEB_PORT", d.port())
	t.Setenv("LEO_API_TOKEN", "token")
	t.Setenv("LEO_PERMISSIONS", "")
	t.Setenv("LEO_DISPATCH_ID", "d-abc123")

	_, err := registryFromEnv().call("leo_surface_file", json.RawMessage(`{"path":"a.go"}`))

	if err == nil || !strings.Contains(err.Error(), "leo_dispatch subagent") || !strings.Contains(err.Error(), "d-abc123") {
		t.Fatalf("err = %v, want a dispatch rejection", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("dispatch call reached the daemon: %+v", d.calls)
	}
}

func TestSurfaceFileDeniable(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "primary", leotools.Permissions{DenyTools: []string{"leo_surface_file"}})
	if slices.Contains(toolNames(reg), "leo_surface_file") {
		t.Fatal("denied tool advertised")
	}
	if _, err := reg.call("leo_surface_file", json.RawMessage(`{"path":"a"}`)); err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("err = %v", err)
	}
}
