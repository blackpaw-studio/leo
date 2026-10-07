package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// permissionDaemon answers the permission long poll with reply and records
// the request it got.
func permissionDaemon(t *testing.T, reply string) (configPath string, got *capturedReport) {
	t.Helper()
	got = &capturedReport{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = capturedReport{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	configPath = filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, got
}

func runPermission(t *testing.T, payload string) string {
	t.Helper()
	cmd := newDispatchPermissionCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("permission hook must never fail: %v", err)
	}
	return out.String()
}

const permissionHookInput = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`

func TestDispatchPermissionPrintsDecision(t *testing.T) {
	clearReportEnv(t)
	configPath, got := permissionDaemon(t, `{"ok":true,"data":{"behavior":"deny","message":"use make test"}}`)
	t.Setenv("LEO_DISPATCH_ID", "d-test")
	t.Setenv("LEO_CONFIG", configPath)
	t.Setenv("LEO_API_TOKEN", "agent-token")

	out := runPermission(t, permissionHookInput)

	if got.path != "/api/dispatch/d-test/permission" || got.auth != "Bearer agent-token" {
		t.Fatalf("request = %+v", *got)
	}
	var sent struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(got.body, &sent); err != nil || string(sent.Payload) != permissionHookInput {
		t.Fatalf("body = %s", got.body)
	}
	var hookOut map[string]any
	if err := json.Unmarshal([]byte(out), &hookOut); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	want := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": map[string]any{"behavior": "deny", "message": "use make test"}}}
	if fmt.Sprint(hookOut) != fmt.Sprint(want) {
		t.Fatalf("stdout = %v, want %v", hookOut, want)
	}
}

func TestDispatchPermissionNoDecisionPrintsNothing(t *testing.T) {
	clearReportEnv(t)
	configPath, _ := permissionDaemon(t, `{"ok":true,"data":{}}`)
	t.Setenv("LEO_DISPATCH_ID", "d-test")
	t.Setenv("LEO_CONFIG", configPath)
	if out := runPermission(t, permissionHookInput); out != "" {
		t.Fatalf("stdout = %q, want nothing so claude shows its own prompt", out)
	}
}

func TestDispatchPermissionOutsideDispatchIsNoop(t *testing.T) {
	clearReportEnv(t)
	if out := runPermission(t, permissionHookInput); out != "" {
		t.Fatalf("stdout = %q outside a dispatch", out)
	}
}

func TestDispatchPermissionDaemonDownPrintsNothing(t *testing.T) {
	clearReportEnv(t)
	configPath := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(configPath, []byte("web:\n  port: 1\ntasks: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEO_DISPATCH_ID", "d-test")
	t.Setenv("LEO_CONFIG", configPath)
	if out := runPermission(t, permissionHookInput); out != "" {
		t.Fatalf("stdout = %q with the daemon down", out)
	}
}

func TestDispatchPermissionMalformedReplyPrintsNothing(t *testing.T) {
	replies := map[string]string{
		"not ok":           `{"ok":false,"data":{"behavior":"allow"}}`,
		"ok missing":       `{"data":{"behavior":"allow"}}`,
		"trailing value":   `{"ok":true,"data":{"behavior":"deny"}}{"ok":true,"data":{"behavior":"allow"}}`,
		"trailing garbage": `{"ok":true,"data":{"behavior":"allow"}} x`,
		"bad behavior":     `{"ok":true,"data":{"behavior":"ask"}}`,
	}
	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			clearReportEnv(t)
			configPath, _ := permissionDaemon(t, reply)
			t.Setenv("LEO_DISPATCH_ID", "d-test")
			t.Setenv("LEO_CONFIG", configPath)
			if out := runPermission(t, permissionHookInput); out != "" {
				t.Fatalf("stdout = %q for reply %s, want nothing", out, reply)
			}
		})
	}
}
