package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Protocol version we negotiate with clients. Matches the spec revision
// Claude Code targets as of late 2025.
const protocolVersion = "2024-11-05"

// jsonRPCMessage is the union of request, response, and notification shapes
// for JSON-RPC 2.0. We decode opportunistically: ID present → request,
// absent → notification; Result/Error present → response.
type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// Run starts the MCP server on stdin/stdout. It reads env (LEO_PROCESS_NAME,
// LEO_WEB_PORT, LEO_API_TOKEN) to bind itself to the supervised process and
// authenticate against the daemon. Returns when stdin closes or a fatal
// error occurs.
func Run() error {
	processName := os.Getenv("LEO_PROCESS_NAME")
	if processName == "" {
		return fmt.Errorf("LEO_PROCESS_NAME not set; leo mcp-server must be launched by a supervised Claude process")
	}
	port := os.Getenv("LEO_WEB_PORT")
	if port == "" {
		return fmt.Errorf("LEO_WEB_PORT not set; the Leo daemon TCP listener must be enabled")
	}
	// LEO_API_TOKEN is required: the daemon's /api/* and /web/* routes are
	// bearer-protected, so an unauthenticated MCP client will fail every
	// request with 401. Refusing to start gives a clearer error than letting
	// every tool call fail opaquely.
	token := os.Getenv("LEO_API_TOKEN")
	if token == "" {
		return fmt.Errorf("LEO_API_TOKEN not set; Leo web auth is required (check that the daemon created ~/.leo/state/api.token)")
	}
	return runWith(os.Stdin, os.Stdout, newRegistry(newDaemonClient(port, token), processName))
}

func runWith(in io.Reader, out io.Writer, reg *registry) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	// Disable Go's default of HTML-escaping <, >, & — MCP clients want raw JSON.
	enc.SetEscapeHTML(false)

	// Protect concurrent writes from streaming responses (we don't stream
	// today, but a single bufio writer keeps line boundaries clean).
	bw := bufio.NewWriter(out)
	enc = json.NewEncoder(bw)
	enc.SetEscapeHTML(false)

	for {
		var msg jsonRPCMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode: %w", err)
		}
		resp, send := dispatch(&msg, reg)
		if !send {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		if err := bw.Flush(); err != nil {
			return fmt.Errorf("flush: %w", err)
		}
	}
}

// dispatch handles a single inbound message. The second return reports
// whether to send the response (false for notifications).
func dispatch(msg *jsonRPCMessage, reg *registry) (jsonRPCMessage, bool) {
	isNotification := len(msg.ID) == 0
	resp := jsonRPCMessage{JSONRPC: "2.0", ID: msg.ID}

	switch msg.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "leo",
				"version": "1",
			},
		}
	case "notifications/initialized":
		// Notifications never get a response.
		return resp, false
	case "tools/list":
		resp.Result = map[string]any{"tools": reg.list()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			resp.Error = &jsonRPCError{Code: codeInvalidRequest, Message: fmt.Sprintf("invalid params: %v", err)}
			break
		}
		text, err := reg.call(params.Name, params.Arguments)
		if err != nil {
			// MCP convention: tool execution errors come back inside the
			// result with isError=true (not as protocol-level errors), so
			// the LLM can see and react to them.
			resp.Result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			break
		}
		resp.Result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		}
	case "ping":
		resp.Result = map[string]any{}
	default:
		if isNotification {
			return resp, false
		}
		resp.Error = &jsonRPCError{Code: codeMethodNotFound, Message: "method not found: " + msg.Method}
	}

	if isNotification {
		return resp, false
	}
	return resp, true
}
