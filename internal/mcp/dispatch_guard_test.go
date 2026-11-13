package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

func TestWaitHeaderReportsTimeWaitedNotJustTheLimit(t *testing.T) {
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		if method == "GET" && path == "/api/dispatch/wait" {
			return 200, `{"ok":true,"data":[{"id":"d-test","status":"closed","elapsed_seconds":1,"active_seconds":1,"turn_id":"d-test#1","outcome":"finished","delivered":true,"text":"done"}]}`
		}
		return 404, `{"ok":false,"error":"nope"}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant", leotools.Permissions{})
	got, err := callTool(reg, "leo_wait", map[string]any{"ids": []any{"d-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "wait timeout") {
		t.Fatalf("header still reads as a timeout: %q", got)
	}
	if !strings.HasPrefix(got, "waited 0s (timeout 1799s)\n") {
		t.Fatalf("header = %q, want the time actually waited with the limit in parentheses", got)
	}
}

func TestSendDispatchNamesItsOwnDispatchAsSender(t *testing.T) {
	var sent map[string]any
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		if method == "POST" && path == "/api/dispatch/d-test/send" {
			_ = json.Unmarshal(body, &sent)
			return 200, `{"ok":true,"data":{"turn_id":"d-test#2","delivered":true}}`
		}
		return 404, `{"ok":false,"error":"nope"}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant", leotools.Permissions{}, withDispatchID("d-parent"))
	if _, err := callTool(reg, "leo_send_dispatch", map[string]any{"id": "d-test", "message": "next"}); err != nil {
		t.Fatal(err)
	}
	if sent["parent_dispatch_id"] != "d-parent" {
		t.Fatalf("sent body = %#v, want the sender's dispatch id so the daemon can apply the read-only guard", sent)
	}
}
