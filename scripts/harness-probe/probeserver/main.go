// Command probeserver is a minimal MCP stdio server used to probe whether a
// coding harness honors notifications/tools/list_changed. It advertises one
// tool (probe_ping); after probe_ping is called it announces a second tool
// (probe_late) via list_changed. probe_late returns a text token plus an 8x8
// red PNG, which also probes image pass-through to the model.
//
// Every initialize, tools/list and tools/call is appended as a JSON line to
// the file named by $PROBE_LOG. See ../README.md.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"sync"
)

const defaultProtocolVersion = "2024-11-05"

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	if err := run(os.Stdin, os.Stdout, os.Getenv("PROBE_LOG")); err != nil {
		fmt.Fprintln(os.Stderr, "probeserver:", err)
		os.Exit(1)
	}
}

type server struct {
	enc      *json.Encoder
	logPath  string
	mu       sync.Mutex
	notified bool
}

func run(in io.Reader, out io.Writer, logPath string) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	s := &server{enc: enc, logPath: logPath}
	dec := json.NewDecoder(in)
	for {
		var msg message
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode: %w", err)
		}
		if err := s.handle(&msg); err != nil {
			return err
		}
	}
}

func (s *server) handle(msg *message) error {
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      any    `json:"clientInfo"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = defaultProtocolVersion
		}
		s.record(map[string]any{"kind": "initialize", "client_info": p.ClientInfo, "protocol_version": p.ProtocolVersion})
		return s.reply(msg, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "probe", "version": "1"},
		})
	case "tools/list":
		s.mu.Lock()
		notified := s.notified
		s.mu.Unlock()
		tools := []map[string]any{toolDef("probe_ping", "Returns pong. Call this first.")}
		if notified {
			tools = append(tools, toolDef("probe_late", "Returns a token and a small image. Only exists after probe_ping was called."))
		}
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t["name"].(string))
		}
		s.record(map[string]any{"kind": "tools_list", "after_notify": notified, "tools": names})
		return s.reply(msg, map[string]any{"tools": tools})
	case "tools/call":
		return s.call(msg)
	case "ping":
		return s.reply(msg, map[string]any{})
	}
	if len(msg.ID) == 0 {
		return nil // notifications get no reply
	}
	return s.write(message{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + msg.Method}})
}

func (s *server) call(msg *message) error {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	s.record(map[string]any{"kind": "tools_call", "name": p.Name})
	switch p.Name {
	case "probe_ping":
		s.mu.Lock()
		s.notified = true
		s.mu.Unlock()
		if err := s.reply(msg, textResult("pong")); err != nil {
			return err
		}
		return s.write(message{JSONRPC: "2.0", Method: "notifications/tools/list_changed"})
	case "probe_late":
		return s.reply(msg, map[string]any{"content": []map[string]any{
			{"type": "text", "text": "PROBE-TOKEN-7731: the image below is a solid colour; report it."},
			{"type": "image", "data": redPNGBase64(), "mimeType": "image/png"},
		}})
	}
	return s.reply(msg, map[string]any{
		"content": []map[string]any{{"type": "text", "text": "unknown tool " + p.Name}},
		"isError": true,
	})
}

func toolDef(name, desc string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": desc,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func textResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func redPNGBase64() string {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func (s *server) reply(req *message, result any) error {
	if len(req.ID) == 0 {
		return nil
	}
	return s.write(message{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *server) write(m message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(m)
}

// record appends one JSONL entry to the probe log. Logging is best-effort: a
// probe that can't log still answers the client.
func (s *server) record(entry map[string]any) {
	if s.logPath == "" {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probeserver: log:", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
