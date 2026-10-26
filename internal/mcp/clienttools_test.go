package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

const clientToolsListBody = `{"ok":true,"data":{"revision":3,"ever_registered":true,"tools":[
 {"group":"browser","name":"browser_click","description":"Click an element by ref.","input_schema":{"type":"object","properties":{"ref":{"type":"string"}},"required":["ref"]},"timeout_s":30},
 {"group":"browser","name":"browser_handoff","description":"Ask the user to take over.","input_schema":{"type":"object","properties":{"message":{"type":"string"}}},"timeout_s":0}
]}}`

// clientToolsDaemon is a fake daemon serving the client-tools agent routes.
type clientToolsDaemon struct {
	mu        sync.Mutex
	listBody  string
	callBody  string
	lastCall  map[string]any
	lastPath  string
	onCall    func(r *http.Request)
	callCount int
}

func newClientToolsDaemon(t *testing.T) (*clientToolsDaemon, *daemonClient) {
	t.Helper()
	d := &clientToolsDaemon{listBody: clientToolsListBody}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/client-tools"):
			d.mu.Lock()
			body := d.listBody
			d.mu.Unlock()
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/client-tools/call"):
			d.mu.Lock()
			d.lastPath = r.URL.Path
			d.callCount++
			_ = json.NewDecoder(r.Body).Decode(&d.lastCall)
			body, hook := d.callBody, d.onCall
			d.mu.Unlock()
			if hook != nil {
				hook(r)
			}
			if body == "" { // the daemon's answer for a name it has never seen
				w.WriteHeader(http.StatusNotFound)
				body = `{"ok":false,"error":"unknown client tool","code":"unknown_tool"}`
			}
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return d, newDaemonClient(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), "tok")
}

func nativeRegistry(client *daemonClient, perms leotools.Permissions) *registry {
	reg := newRegistry(client, "alfred", perms)
	reg.setClientName("claude-code")
	return reg
}

func lookupTool(defs []toolDef, name string) (toolDef, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d, true
		}
	}
	return toolDef{}, false
}

func TestToolsListIncludesRegisteredClientTools(t *testing.T) {
	_, client := newClientToolsDaemon(t)
	reg := nativeRegistry(client, leotools.Permissions{})
	defs := reg.list()
	click, ok := lookupTool(defs, "browser_click")
	if !ok {
		t.Fatalf("browser_click missing from %d tools", len(defs))
	}
	if click.Description != "Click an element by ref." || click.InputSchema["type"] != "object" {
		t.Fatalf("browser_click = %+v", click)
	}
	if _, ok := lookupTool(defs, "leo_skill"); !ok {
		t.Fatal("built-in tools must stay listed")
	}
	if _, ok := lookupTool(defs, "browser_handoff"); !ok {
		t.Fatal("browser_handoff missing")
	}
}

func TestToolsListHasNoClientToolsWhenNoneRegistered(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	d.listBody = `{"ok":true,"data":{"revision":0,"ever_registered":false,"tools":[]}}`
	reg := nativeRegistry(client, leotools.Permissions{})
	for _, def := range reg.list() {
		if strings.HasPrefix(def.Name, "browser_") || strings.HasPrefix(def.Name, "leo_app_") {
			t.Fatalf("unexpected tool %s on a daemon with no client tools", def.Name)
		}
	}
}

func TestClientToolCallReturnsImageBlock(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	content := `[{"type":"text","text":"clicked e12"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]`
	d.callBody = `{"ok":true,"data":{"ok":true,"content":` + content + `}}`
	reg := nativeRegistry(client, leotools.Permissions{})

	out := reg.callTool(context.Background(), "browser_click", json.RawMessage(`{"ref":"e12"}`))
	if out.IsError || !bytes.Equal(out.Content, []byte(content)) {
		t.Fatalf("outcome = %+v / %s", out, out.Content)
	}
	if d.lastPath != "/api/agent/alfred/client-tools/call" {
		t.Fatalf("path = %q", d.lastPath)
	}
	args, _ := d.lastCall["args"].(map[string]any)
	if d.lastCall["tool"] != "browser_click" || args["ref"] != "e12" {
		t.Fatalf("call body = %v", d.lastCall)
	}
}

func TestClientToolImageBlockSurvivesTheMCPWire(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	d.callBody = `{"ok":true,"data":{"ok":true,"content":[{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}`
	reg := nativeRegistry(client, leotools.Permissions{})

	resp := roundTrip(t, reg, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"browser_click","arguments":{"ref":"e1"}}}`)
	var result struct {
		Content []struct {
			Type     string `json:"type"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "image" || result.Content[0].MimeType != "image/png" || result.IsError {
		t.Fatalf("result = %s", resp.Result)
	}
}

// roundTrip sends one request through runWith and returns its response. stdin
// stays open until the response arrives: EOF cancels in-flight calls.
func roundTrip(t *testing.T, reg *registry, request string) struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
} {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := runWith(inR, outW, reg)
		_ = outW.Close()
		done <- err
	}()
	if _, err := io.WriteString(inW, request+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(outR).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	_ = inW.Close()
	<-done
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decoding %q: %v", line, err)
	}
	return resp
}

func TestClientToolErrorResultIsReportedAsToolError(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	d.callBody = `{"ok":true,"data":{"ok":false,"error":{"code":"stale_ref","message":"ref e12 is not in the current snapshot"}}}`
	reg := nativeRegistry(client, leotools.Permissions{})
	out := reg.callTool(context.Background(), "browser_click", json.RawMessage(`{"ref":"e12"}`))
	if !out.IsError {
		t.Fatalf("outcome = %+v", out)
	}
	var blocks []struct{ Type, Text string }
	if err := json.Unmarshal(out.Content, &blocks); err != nil || len(blocks) != 1 || blocks[0].Text != "stale_ref: ref e12 is not in the current snapshot" {
		t.Fatalf("content = %s", out.Content)
	}
}

func TestDeniedClientToolHiddenAndRefused(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	d.callBody = `{"ok":true,"data":{"ok":true,"content":[]}}`
	reg := nativeRegistry(client, leotools.Permissions{DenyTools: []string{"browser_click"}})

	if _, ok := lookupTool(reg.list(), "browser_click"); ok {
		t.Fatal("a denied client tool is listed")
	}
	if _, ok := lookupTool(reg.list(), "browser_handoff"); !ok {
		t.Fatal("an allowed client tool vanished")
	}
	out := reg.callTool(context.Background(), "browser_click", json.RawMessage(`{}`))
	if !out.IsError || !strings.Contains(string(out.Content), "not permitted") {
		t.Fatalf("outcome = %s", out.Content)
	}
	if d.callCount != 0 {
		t.Fatal("a denied call reached the daemon")
	}
}

// outcomeText is the text of an outcome's first content block.
func outcomeText(t *testing.T, o toolOutcome) string {
	t.Helper()
	var blocks []struct{ Text string }
	if err := json.Unmarshal(o.Content, &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("content = %s (%v)", o.Content, err)
	}
	return blocks[0].Text
}

func TestUnknownToolStillReportsUnknown(t *testing.T) {
	_, client := newClientToolsDaemon(t)
	reg := nativeRegistry(client, leotools.Permissions{})
	out := reg.callTool(context.Background(), "no_such_tool", json.RawMessage(`{}`))
	if got := outcomeText(t, out); !out.IsError || got != `unknown tool "no_such_tool"` {
		t.Fatalf("outcome = %q (error=%v)", got, out.IsError)
	}
}

func TestCancelledNotificationAbortsClientCall(t *testing.T) {
	d, client := newClientToolsDaemon(t)
	entered := make(chan struct{})
	aborted := make(chan struct{})
	d.onCall = func(r *http.Request) {
		close(entered)
		<-r.Context().Done() // the daemon sees the MCP call go away
		close(aborted)
	}
	d.callBody = `{"ok":true,"data":{"ok":false,"error":{"code":"cancelled","message":"cancelled"}}}`
	reg := nativeRegistry(client, leotools.Permissions{})

	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runWith(reader, &out, reg) }()
	enc := json.NewEncoder(writer)
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": "browser_handoff", "arguments": map[string]any{"message": "log in"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("call never reached the daemon")
	}
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 9}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon call was not cancelled")
	}
	_ = writer.Close()
	<-done
}

func TestClientToolCallDeadlineIsTimeoutPlusGrace(t *testing.T) {
	if got := clientToolCallTimeout(30); got != 35*time.Second {
		t.Fatalf("timeout 30 -> %v, want 35s", got)
	}
	if got := clientToolCallTimeout(0); got != 0 {
		t.Fatalf("timeout 0 -> %v, want none", got)
	}
}
