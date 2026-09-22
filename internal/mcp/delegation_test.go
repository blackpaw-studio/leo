package mcp

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

func TestRoleDispatchChecksResolvedTemplatePermission(t *testing.T) {
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/api/delegation/resolve" {
			return 200, `{"ok":true,"data":{"role":"implement","profile":"p","template":"allowed"}}`
		}
		return 200, `{"ok":true,"data":{"id":"d-1","harness":"claude","model":"sonnet","cwd":"/tmp"}}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{CanConsult: []string{"allowed"}})
	text, err := callTool(reg, "leo_dispatch", map[string]any{"role": "implement", "prompt": "go", "cwd": "/tmp"})
	if err != nil || !strings.Contains(text, "implement→allowed") {
		t.Fatalf("text=%q err=%v", text, err)
	}
	calls := d.recorded()
	if len(calls) != 2 || calls[1].Path != "/api/dispatch" || !strings.Contains(calls[1].Body, `"expect_template":"allowed"`) {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestRoleDispatchDeniedBeforePost(t *testing.T) {
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/api/delegation/resolve" {
			return 200, `{"ok":true,"data":{"template":"forbidden"}}`
		}
		return 200, `{"ok":true,"data":{}}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{CanConsult: []string{"allowed"}})
	if _, err := callTool(reg, "leo_dispatch", map[string]any{"role": "implement", "prompt": "go", "cwd": "/tmp"}); err == nil {
		t.Fatal("expected permission denial")
	}
	if calls := d.recorded(); len(calls) != 1 {
		t.Fatalf("calls=%#v", calls)
	}
}
