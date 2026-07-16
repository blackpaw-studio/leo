package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
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
// authenticate against the daemon. When the daemon's TCP listener isn't
// available (LEO_WEB_PORT/LEO_API_TOKEN unset), it degrades to local-only
// mode instead of refusing to start: purely local tools (leo_skill) still
// work, daemon-backed tools are simply omitted from the registry. Returns
// when stdin closes or a fatal error occurs.
func Run() error {
	return runWith(os.Stdin, os.Stdout, registryFromEnv())
}

// registryFromEnv builds the tool registry from the process environment.
// Full mode (daemon-backed tools + leo_skill) requires both LEO_WEB_PORT and
// LEO_API_TOKEN — the daemon's /api/* and /web/* routes are bearer-protected,
// so an unauthenticated client would fail every daemon call with 401 anyway.
// If either is missing, it falls back to local-only mode (leo_skill only)
// rather than erroring, so the MCP server still starts when the web listener
// is disabled. LEO_PROCESS_NAME may be empty in local-only mode since no
// registered tool in that mode uses it.
func registryFromEnv() *registry {
	processName := os.Getenv("LEO_PROCESS_NAME")
	port := os.Getenv("LEO_WEB_PORT")
	token := os.Getenv("LEO_API_TOKEN")

	if port == "" || token == "" {
		fmt.Fprintln(os.Stderr, "leo mcp-server: local-only mode (daemon listener unavailable)")
		return newRegistry(nil, processName)
	}

	fmt.Fprintln(os.Stderr, "leo mcp-server: full mode (daemon listener available)")
	return newRegistry(newDaemonClient(port, token), processName)
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

	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	var wg sync.WaitGroup
	var writeMu sync.Mutex
	var requestsMu sync.Mutex
	requests := make(map[string]context.CancelFunc)
	var writeErr error

	writeResponse := func(resp jsonRPCMessage) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr != nil {
			return
		}
		if err := enc.Encode(resp); err != nil {
			writeErr = fmt.Errorf("encode: %w", err)
			return
		}
		if err := bw.Flush(); err != nil {
			writeErr = fmt.Errorf("flush: %w", err)
		}
	}

	for {
		var msg jsonRPCMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				cancelAll()
				wg.Wait()
				return writeErr
			}
			cancelAll()
			wg.Wait()
			return fmt.Errorf("decode: %w", err)
		}

		if msg.Method == "notifications/cancelled" {
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(msg.Params, &params) == nil {
				requestsMu.Lock()
				cancel := requests[requestKey(params.RequestID)]
				requestsMu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			continue
		}

		requestCtx, cancel := context.WithCancel(ctx)
		key := requestKey(msg.ID)
		if key != "" {
			requestsMu.Lock()
			requests[key] = cancel
			requestsMu.Unlock()
		}
		wg.Add(1)
		go func(msg jsonRPCMessage) {
			defer wg.Done()
			defer cancel()
			if key != "" {
				defer func() {
					requestsMu.Lock()
					delete(requests, key)
					requestsMu.Unlock()
				}()
			}
			resp, send := dispatch(requestCtx, &msg, reg)
			if send {
				writeResponse(resp)
			}
		}(msg)
	}
}

func requestKey(id json.RawMessage) string {
	var compact bytes.Buffer
	if json.Compact(&compact, id) == nil {
		return compact.String()
	}
	return string(id)
}

// dispatch handles a single inbound message. The second return reports
// whether to send the response (false for notifications).
func dispatch(ctx context.Context, msg *jsonRPCMessage, reg *registry) (jsonRPCMessage, bool) {
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
		text, err := reg.callContext(ctx, params.Name, params.Arguments)
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
