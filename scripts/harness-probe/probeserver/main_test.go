package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// harness drives run() over pipes the way an MCP client would.
type harness struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
}

func startServer(t *testing.T, logPath string) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(inR, outW, logPath)
		outW.Close()
		done <- err
	}()
	t.Cleanup(func() {
		inW.Close()
		<-done
	})
	return &harness{t: t, in: inW, out: bufio.NewReader(outR)}
}

func (h *harness) send(v string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, v+"\n"); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func (h *harness) recv() rpc {
	h.t.Helper()
	line, err := h.out.ReadString('\n')
	if err != nil {
		h.t.Fatalf("recv: %v", err)
	}
	var m rpc
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		h.t.Fatalf("recv decode %q: %v", line, err)
	}
	return m
}

func toolNames(t *testing.T, result json.RawMessage) []string {
	t.Helper()
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(r.Tools))
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestProbeEmitsListChangedAfterPing(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "probe.jsonl")
	h := startServer(t, logPath)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"fake-harness","version":"9.9"}}}`)
	init := h.recv()
	var ir struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(init.Result, &ir); err != nil {
		t.Fatal(err)
	}
	if ir.ProtocolVersion != "2025-06-18" || !ir.Capabilities.Tools.ListChanged {
		t.Fatalf("initialize result = %s", init.Result)
	}
	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if got := toolNames(t, h.recv().Result); strings.Join(got, ",") != "probe_ping" {
		t.Fatalf("first list = %v", got)
	}

	h.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"probe_ping","arguments":{}}}`)
	if resp := h.recv(); string(resp.ID) != "3" {
		t.Fatalf("expected ping response first, got %+v", resp)
	}
	if note := h.recv(); note.Method != "notifications/tools/list_changed" || len(note.ID) != 0 {
		t.Fatalf("expected list_changed notification, got %+v", note)
	}

	h.send(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if got := toolNames(t, h.recv().Result); strings.Join(got, ",") != "probe_ping,probe_late" {
		t.Fatalf("second list = %v", got)
	}

	h.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"probe_late","arguments":{}}}`)
	var call struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"content"`
	}
	if err := json.Unmarshal(h.recv().Result, &call); err != nil {
		t.Fatal(err)
	}
	if len(call.Content) != 2 || call.Content[0].Type != "text" || call.Content[1].Type != "image" ||
		call.Content[1].MimeType != "image/png" || call.Content[1].Data == "" {
		t.Fatalf("probe_late content = %+v", call.Content)
	}

	h.in.Close()
	logged := readLog(t, logPath)
	wantKinds := []string{"initialize", "tools_list", "tools_call", "tools_list", "tools_call"}
	if len(logged) != len(wantKinds) {
		t.Fatalf("log has %d entries, want %d: %v", len(logged), len(wantKinds), logged)
	}
	for i, k := range wantKinds {
		if logged[i]["kind"] != k {
			t.Errorf("log[%d].kind = %v, want %s", i, logged[i]["kind"], k)
		}
	}
	if ci, _ := logged[0]["client_info"].(map[string]any); ci["name"] != "fake-harness" {
		t.Errorf("client_info = %v", logged[0]["client_info"])
	}
	if logged[1]["after_notify"] != false || logged[3]["after_notify"] != true {
		t.Errorf("after_notify flags = %v, %v", logged[1]["after_notify"], logged[3]["after_notify"])
	}
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}
