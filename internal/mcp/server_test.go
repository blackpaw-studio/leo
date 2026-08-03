package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon stands in for the Leo daemon's TCP listener and records the
// requests it receives so tests can assert on dispatch.
type fakeDaemon struct {
	mu      sync.Mutex
	calls   []recordedCall
	srv     *httptest.Server
	respond func(method, path string, body []byte) (int, string)
}

func TestRunWithDispatchesRequestsConcurrently(t *testing.T) {
	reg := &registry{
		handlers:           make(map[string]toolHandler),
		contextualHandlers: make(map[string]func(context.Context, map[string]any) (string, error)),
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	reg.addContext(toolDef{Name: "slow"}, func(context.Context, map[string]any) (string, error) {
		started <- struct{}{}
		<-release
		return "done", nil
	})

	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runWith(reader, &out, reg) }()
	enc := json.NewEncoder(writer)
	for id := 1; id <= 2; id++ {
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "slow", "arguments": map[string]any{}}}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("second request did not start concurrently")
		}
	}
	close(release)
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 2 {
		t.Fatalf("got %d responses: %s", lines, out.String())
	}
}

func TestRunWithCancellationNotificationCancelsTool(t *testing.T) {
	reg := &registry{
		handlers:           make(map[string]toolHandler),
		contextualHandlers: make(map[string]func(context.Context, map[string]any) (string, error)),
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	reg.addContext(toolDef{Name: "slow"}, func(ctx context.Context, _ map[string]any) (string, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return "", ctx.Err()
	})
	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runWith(reader, &out, reg) }()
	enc := json.NewEncoder(writer)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "slow", "arguments": map[string]any{}}})
	<-started
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 7}})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("tool context was not cancelled")
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatalf("runWith: %v", err)
	}
}

func TestRunWithEOFCancelsInflightTool(t *testing.T) {
	reg := &registry{
		handlers:           make(map[string]toolHandler),
		contextualHandlers: make(map[string]func(context.Context, map[string]any) (string, error)),
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	reg.addContext(toolDef{Name: "slow"}, func(ctx context.Context, _ map[string]any) (string, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return "", ctx.Err()
	})
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- runWith(reader, io.Discard, reg) }()
	_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": "slow", "arguments": map[string]any{}}})
	<-started
	_ = writer.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stdin EOF did not cancel the in-flight tool")
	}
	if err := <-done; err != nil {
		t.Fatalf("runWith: %v", err)
	}
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

// runRequest dispatches one request directly. Stream lifecycle behavior is
// covered separately because EOF intentionally cancels in-flight requests.
func runRequest(t *testing.T, reg *registry, req map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var msg jsonRPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	rpcResp, send := dispatch(context.Background(), &msg, reg)
	if !send {
		return nil
	}
	out, err := json.Marshal(rpcResp)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v (raw: %s)", err, out)
	}
	return resp
}

func TestInitializeReturnsServerInfo(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "test-process")
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
	reg := newRegistry(newDaemonClient("0", ""), "test-process")
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
	reg := newRegistry(newDaemonClient("0", ""), "test-process")
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
		"leo_send_message", "leo_skill", "leo_consult",
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
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary")

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
	if c.Method != http.MethodPost || c.Path != "/web/agent/primary/send" {
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
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary")

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

func TestToolCallSpawnAgentWithoutRepoSucceeds(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":true,"data":{"name":"assistant","workspace":"/tmp/assistant"}}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary")

	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "leo_spawn_agent",
			"arguments": map[string]any{"template": "assistant"},
		},
	})
	result := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("expected success spawning without repo, got %+v", result)
	}
	c := daemon.calls[0]
	if c.Path != "/api/agent/spawn" {
		t.Errorf("wrong path: %s", c.Path)
	}
	if !strings.Contains(c.Body, `"template":"assistant"`) {
		t.Errorf("body missing template: %s", c.Body)
	}
}

func TestToolCallReturnsIsErrorOnDaemonFailure(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":false,"error":"task not found"}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary")

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
	reg := newRegistry(newDaemonClient("0", ""), "primary")
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
	reg := newRegistry(newDaemonClient("0", ""), "primary")
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

func TestRegistryFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		port        string
		token       string
		processName string
		wantFull    bool
	}{
		{
			name:        "port and token set builds full registry",
			port:        "12345",
			token:       "secret",
			processName: "primary",
			wantFull:    true,
		},
		{
			name:  "missing port falls back to local-only",
			token: "secret",
		},
		{
			name: "missing token falls back to local-only",
			port: "12345",
		},
		{
			name: "both missing falls back to local-only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LEO_WEB_PORT", tt.port)
			t.Setenv("LEO_API_TOKEN", tt.token)
			t.Setenv("LEO_PROCESS_NAME", tt.processName)

			reg := registryFromEnv()

			_, hasDaemonTool := reg.handlers["leo_list_tasks"]
			if hasDaemonTool != tt.wantFull {
				t.Errorf("daemon tool present = %v, want %v", hasDaemonTool, tt.wantFull)
			}
			if _, ok := reg.handlers["leo_skill"]; !ok {
				t.Errorf("leo_skill should always be registered")
			}
		})
	}
}

func TestSendMessageDeliversWithSenderPrefix(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":true,"data":{}}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "sender-proc")

	out, err := reg.call("leo_send_message", json.RawMessage(`{"to":"worker-1","message":"ping"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	if len(daemon.calls) != 1 {
		t.Fatalf("expected 1 daemon call, got %d", len(daemon.calls))
	}
	c := daemon.calls[0]
	if c.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", c.Method)
	}
	if c.Path != "/web/agent/worker-1/message" {
		t.Errorf("expected path /web/agent/worker-1/message, got %s", c.Path)
	}
	if !strings.Contains(c.Body, "sender-proc") || !strings.Contains(c.Body, "ping") {
		t.Errorf("delivered body should carry sender + message; got %q", c.Body)
	}
	if !strings.Contains(out, "worker-1") {
		t.Errorf("result should confirm target; got %q", out)
	}
}

func TestSendMessageRejectsSelf(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "self-proc")
	_, err := reg.call("leo_send_message", json.RawMessage(`{"to":"self-proc","message":"hi"}`))
	if err == nil {
		t.Fatal("expected error when messaging self")
	}
	if !strings.Contains(err.Error(), "self-proc") {
		t.Errorf("error should mention the self name; got %v", err)
	}
}

func TestSendMessageRequiresMessage(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "self-proc")
	_, err := reg.call("leo_send_message", json.RawMessage(`{"to":"worker-1"}`))
	if err == nil {
		t.Fatal("expected error when message missing")
	}
	if !strings.Contains(err.Error(), "message") {
		t.Errorf("error should mention the missing field; got %v", err)
	}
}

func TestLeoConsultReturnsResult(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		gotPath = method + " " + path
		json.Unmarshal(body, &gotBody)
		return 200, `{"ok":true,"data":{"harness":"codex","model":"gpt-5.6-sol","text":"review looks good"}}`
	})
	defer d.close()

	reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "leo_consult",
			"arguments": map[string]any{"template": "codex", "prompt": "opinion?"},
		},
	})

	if gotPath != "POST /api/consult" {
		t.Fatalf("daemon call %q", gotPath)
	}
	if gotBody["from"] != "assistant" || gotBody["template"] != "codex" || gotBody["prompt"] != "opinion?" {
		t.Fatalf("body %+v", gotBody)
	}
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "review looks good") || !strings.Contains(content["text"].(string), "codex/gpt-5.6-sol") {
		t.Fatalf("tool result %v", content)
	}
}

func TestLeoConsultDispatchesWithModelOverride(t *testing.T) {
	var gotBody map[string]string
	d := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		json.Unmarshal(body, &gotBody)
		return 200, `{"ok":true,"data":{"harness":"codex","model":"gpt-x","text":"answer"}}`
	})
	defer d.close()

	reg := newRegistry(newDaemonClient(d.port(), "tok"), "assistant")
	runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "leo_consult",
			"arguments": map[string]any{"template": "codex", "prompt": "opinion?", "model": "gpt-x"},
		},
	})

	if gotBody["model"] != "gpt-x" {
		t.Fatalf("daemon POST body model = %q, want gpt-x; body %+v", gotBody["model"], gotBody)
	}
}

// TestConsultAndMessageDescriptionsCrossReference guards the copy that keeps
// "consult fable" from being mis-routed to leo_send_message: each tool must
// name the other so the model can tell a template from a running agent.
func TestConsultAndMessageDescriptionsCrossReference(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "assistant")
	desc := make(map[string]string)
	for _, d := range reg.list() {
		desc[d.Name] = d.Description
	}

	consult := strings.ToLower(desc["leo_consult"])
	for _, want := range []string{"second opinion", "leo_send_message"} {
		if !strings.Contains(consult, want) {
			t.Errorf("leo_consult description missing %q; got %q", want, desc["leo_consult"])
		}
	}
	if !strings.Contains(desc["leo_send_message"], "leo_consult") {
		t.Errorf("leo_send_message description should point at leo_consult; got %q", desc["leo_send_message"])
	}
}

func TestSkillToolWithNoArgsListsCatalog(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "primary")
	out, err := reg.call("leo_skill", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	for _, name := range []string{"managing-tasks", "debugging-logs", "daemon-management", "config-reference", "workspace-maintenance", "agent-management"} {
		if !strings.Contains(out, name) {
			t.Errorf("catalog listing missing skill %q; got %q", name, out)
		}
	}
}

func TestSkillToolByNameReturnsFullContent(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "primary")

	for _, name := range []string{"managing-tasks", "managing-tasks.md"} {
		t.Run(name, func(t *testing.T) {
			out, err := reg.call("leo_skill", json.RawMessage(`{"name":"`+name+`"}`))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if !strings.Contains(out, "# Managing Tasks") {
				t.Errorf("expected full skill content with heading; got %q", out)
			}
		})
	}
}

func TestLocalOnlyRegistryOmitsDaemonTools(t *testing.T) {
	reg := newRegistry(nil, "")
	resp := runRequest(t, reg, map[string]any{
		"jsonrpc": "2.0",
		"id":      8,
		"method":  "tools/list",
	})
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)

	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.(map[string]any)["name"].(string)] = true
	}
	if !got["leo_skill"] {
		t.Errorf("expected leo_skill in local-only registry, got %v", got)
	}
	if got["leo_list_tasks"] {
		t.Errorf("expected daemon tool leo_list_tasks to be absent in local-only registry, got %v", got)
	}
}

func TestLocalOnlyRegistryCanCallSkillTool(t *testing.T) {
	reg := newRegistry(nil, "")
	out, err := reg.call("leo_skill", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(out, "managing-tasks") {
		t.Errorf("expected skill catalog listing, got %q", out)
	}
}

func TestSkillToolUnknownNameListsValidNames(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "primary")
	_, err := reg.call("leo_skill", json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for unknown skill name")
	}
	for _, name := range []string{"managing-tasks", "daemon-management"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should list valid names, missing %q; got %v", name, err)
		}
	}
}

// TestSendMessageSendsSenderIdentityStructurally: the observability API needs
// to know WHO sent a message without inspecting content, so leo_send_message
// carries its process name as a real `from` field. The delivered text must be
// unchanged — recipients still see the prefix.
func TestSendMessageSendsSenderIdentityStructurally(t *testing.T) {
	daemon := newFakeDaemon(func(method, path string, body []byte) (int, string) {
		return http.StatusOK, `{"ok":true,"data":{}}`
	})
	defer daemon.close()
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "sender-proc")

	if _, err := reg.call("leo_send_message", json.RawMessage(`{"to":"worker-1","message":"ping"}`)); err != nil {
		t.Fatalf("call: %v", err)
	}

	var sent struct {
		Text string `json:"text"`
		From string `json:"from"`
	}
	if err := json.Unmarshal([]byte(daemon.calls[0].Body), &sent); err != nil {
		t.Fatalf("decoding delivered body: %v", err)
	}
	if sent.From != "sender-proc" {
		t.Errorf("from = %q, want the sending process name", sent.From)
	}
	if want := fmt.Sprintf(msgPrefixFormat, "sender-proc", "ping"); sent.Text != want {
		t.Errorf("delivered text = %q, want %q (text must not change)", sent.Text, want)
	}
}
