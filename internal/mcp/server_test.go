package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon stands in for the Leo daemon's TCP listener and records the
// requests it receives so tests can assert on dispatch.
type fakeDaemon struct {
	mu       sync.Mutex
	calls    []recordedCall
	srv      *httptest.Server
	respond  func(method, path string, body []byte) (int, string)
}

type recordedCall struct {
	Method string
	Path   string
	Body   string
}

func newFakeDaemon(respond func(method, path string, body []byte) (int, string)) *fakeDaemon {
	d := &fakeDaemon{respond: respond}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.calls = append(d.calls, recordedCall{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		d.mu.Unlock()
		status, payload := d.respond(r.Method, r.URL.Path, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	return d
}

func (d *fakeDaemon) port() string {
	// httptest.Server URL is http://127.0.0.1:PORT — we want the PORT.
	return strings.TrimPrefix(d.srv.URL, "http://127.0.0.1:")
}

func (d *fakeDaemon) close() { d.srv.Close() }

// runRequest pipes a single JSON-RPC request through the server and returns
// the decoded response, dispatching against the supplied registry.
func runRequest(t *testing.T, reg *registry, req map[string]any) map[string]any {
	t.Helper()
	in := &bytes.Buffer{}
	if err := json.NewEncoder(in).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	out := &bytes.Buffer{}
	if err := runWith(in, out, reg); err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if out.Len() == 0 {
		return nil
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (raw: %s)", err, out.String())
	}
	return resp
}

func TestInitializeReturnsServerInfo(t *testing.T) {
	reg := newRegistry(newDaemonClient("0"), "test-process")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	server, _ := result["serverInfo"].(map[string]any)
	if server["name"] != "leo" {
		t.Errorf("serverInfo.name = %v, want leo", server["name"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("expected tools capability")
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	reg := newRegistry(newDaemonClient("0"), "test-process")
	in := &bytes.Buffer{}
	json.NewEncoder(in).Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	out := &bytes.Buffer{}
	if err := runWith(in, out, reg); err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("notification should produce no output; got %s", out.String())
	}
}

func TestToolsListContainsCanonicalCommands(t *testing.T) {
	reg := newRegistry(newDaemonClient("0"), "test-process")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)

	want := []string{
		"leo_clear", "leo_compact", "leo_interrupt",
		"leo_list_tasks", "leo_run_task", "leo_toggle_task",
		"leo_list_templates", "leo_spawn_agent", "leo_list_agents", "leo_stop_agent",
	}
	got := map[string]bool{}
	for _, t := range tools {
		got[t.(map[string]any)["name"].(string)] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestToolCallClearSendsKeystrokes(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":true}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port()), "primary")

	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "leo_clear",
			"arguments": map[string]any{},
		},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Errorf("tool call should not be an error: %+v", result)
	}

	if len(daemon.calls) != 1 {
		t.Fatalf("expected 1 daemon call, got %d", len(daemon.calls))
	}
	c := daemon.calls[0]
	if c.Method != http.MethodPost || c.Path != "/web/process/primary/send" {
		t.Errorf("wrong call: %+v", c)
	}
	var sent struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(c.Body), &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if len(sent.Keys) != 2 || sent.Keys[0] != "/clear" || sent.Keys[1] != "Enter" {
		t.Errorf("expected keys=[/clear, Enter], got %v", sent.Keys)
	}
}

func TestToolCallSpawnAgentRoundtrips(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":true,"data":{"name":"agent-1","workspace":"/tmp/a"}}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port()), "primary")

	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "leo_spawn_agent",
			"arguments": map[string]any{"template": "coding", "repo": "owner/repo"},
		},
	})
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("expected success, got %+v", result)
	}
	c := daemon.calls[0]
	if c.Path != "/api/agent/spawn" {
		t.Errorf("wrong path: %s", c.Path)
	}
	if !strings.Contains(c.Body, `"template":"coding"`) || !strings.Contains(c.Body, `"repo":"owner/repo"`) {
		t.Errorf("body missing fields: %s", c.Body)
	}
}

func TestToolCallReturnsIsErrorOnDaemonFailure(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":false,"error":"task not found"}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port()), "primary")

	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "leo_run_task",
			"arguments": map[string]any{"name": "missing"},
		},
	})
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("expected isError=true, got %+v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "task not found") {
		t.Errorf("error text should mention reason; got %v", content)
	}
}

func TestToolCallMissingRequiredArgFails(t *testing.T) {
	reg := newRegistry(newDaemonClient("0"), "primary")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      6,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "leo_run_task",
			"arguments": map[string]any{},
		},
	})
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("expected isError=true for missing arg; got %+v", result)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	reg := newRegistry(newDaemonClient("0"), "primary")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "no/such/method",
	})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error response: %+v", resp)
	}
	if int(errObj["code"].(float64)) != codeMethodNotFound {
		t.Errorf("expected codeMethodNotFound, got %v", errObj["code"])
	}
}
