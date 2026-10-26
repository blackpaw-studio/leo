package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// clientToolListTimeout bounds the daemon round trip tools/list makes for the
// client-tools set; on failure the list simply has none of them.
const clientToolListTimeout = 5 * time.Second

// clientToolCallGrace is added to a client tool's own timeout_s for the HTTP
// round trip, so the daemon's structured `timeout` error arrives before the
// request is abandoned.
const clientToolCallGrace = 5 * time.Second

// clientToolInfo is one client tool as the daemon lists it.
type clientToolInfo struct {
	Group       string          `json:"group"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	TimeoutS    int             `json:"timeout_s"`
}

// clientToolList is the daemon's client-tools listing.
type clientToolList struct {
	Revision       uint64           `json:"revision"`
	EverRegistered bool             `json:"ever_registered"`
	Tools          []clientToolInfo `json:"tools"`
}

// toolOutcome is a tools/call result: MCP content blocks, as raw JSON, and
// whether the tool reports failure.
type toolOutcome struct {
	Content json.RawMessage
	IsError bool
}

// textOutcome wraps text in a single text block.
func textOutcome(text string, isError bool) toolOutcome {
	blocks, _ := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	return toolOutcome{Content: blocks, IsError: isError}
}

// mcpResult is the tools/call result object.
func (o toolOutcome) mcpResult() map[string]any {
	res := map[string]any{"content": o.Content}
	if o.IsError {
		res["isError"] = true
	}
	return res
}

// clientToolCallTimeout is how long to wait on the daemon for a tool with
// timeout_s seconds; 0 (a tool with no timeout) waits for the call's context.
func clientToolCallTimeout(timeoutS int) time.Duration {
	if timeoutS <= 0 {
		return 0
	}
	return time.Duration(timeoutS)*time.Second + clientToolCallGrace
}

// clientToolsView is this agent's window onto the daemon's client tools: it
// lists them for tools/list and runs calls through the daemon's relay.
type clientToolsView struct {
	client *daemonClient
	agent  string
	perms  leotools.Permissions

	mu    sync.Mutex
	known map[string]clientToolInfo // by name, as of the last listing

	// listed and listedRev are the revision the harness last saw in
	// tools/list (or was last told had changed): what list_changed is
	// measured against.
	listed    bool
	listedRev uint64
}

func newClientToolsView(client *daemonClient, agent string, perms leotools.Permissions) *clientToolsView {
	return &clientToolsView{client: client, agent: agent, perms: perms, known: map[string]clientToolInfo{}}
}

// fetch lists the daemon's client tools, remembering them for calls.
func (v *clientToolsView) fetch(ctx context.Context) (clientToolList, error) {
	ctx, cancel := context.WithTimeout(ctx, clientToolListTimeout)
	defer cancel()
	list, err := v.client.clientTools(ctx, v.agent)
	if err != nil {
		return clientToolList{}, err
	}
	known := make(map[string]clientToolInfo, len(list.Tools))
	for _, t := range list.Tools {
		known[t.Name] = t
	}
	v.mu.Lock()
	v.known = known
	v.mu.Unlock()
	return list, nil
}

// defs returns the client tools as MCP tool definitions, minus any the
// agent's template denies. A listing failure yields none.
func (v *clientToolsView) defs(ctx context.Context) []toolDef {
	list, err := v.fetch(ctx)
	if err != nil {
		return nil
	}
	v.mu.Lock()
	v.listed, v.listedRev = true, list.Revision
	v.mu.Unlock()
	return v.defsOf(list.Tools)
}

func (v *clientToolsView) defsOf(tools []clientToolInfo) []toolDef {
	defs := make([]toolDef, 0, len(tools))
	for _, t := range tools {
		if v.perms.DeniesTool(t.Name) {
			continue
		}
		var schema map[string]any
		if json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, toolDef{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return defs
}

// lookup returns a client tool's info, refreshing the listing once if the
// name is not known yet.
func (v *clientToolsView) lookup(ctx context.Context, name string) (clientToolInfo, bool) {
	v.mu.Lock()
	t, ok := v.known[name]
	v.mu.Unlock()
	if ok {
		return t, true
	}
	if _, err := v.fetch(ctx); err != nil {
		return clientToolInfo{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok = v.known[name]
	return t, ok
}

// call runs a client tool through the daemon and maps the result: the app's
// content blocks verbatim on success, "<code>: <message>" as a tool error
// otherwise.
func (v *clientToolsView) call(ctx context.Context, name string, args json.RawMessage) toolOutcome {
	if v.perms.DeniesTool(name) {
		return textOutcome(fmt.Sprintf("tool %q is not permitted for this agent", name), true)
	}
	info, _ := v.lookup(ctx, name)
	if d := clientToolCallTimeout(info.TimeoutS); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	res, err := v.client.callClientTool(ctx, v.agent, name, args)
	if err != nil {
		if errIsUnknownClientTool(err) {
			return textOutcome(fmt.Sprintf("unknown tool %q", name), true)
		}
		return textOutcome(err.Error(), true)
	}
	if !res.OK {
		msg := "client tool failed"
		if res.Error != nil {
			msg = res.Error.Code + ": " + res.Error.Message
		}
		return textOutcome(msg, true)
	}
	if len(res.Content) == 0 {
		return textOutcome("", false)
	}
	return toolOutcome{Content: res.Content}
}
