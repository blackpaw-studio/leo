package observe

import (
	"encoding/json"
	"time"
)

// Client-tools relay events (docs/configuration/client-tools.md). The relay itself
// lives in internal/clienttools; these are its wire shapes.
const (
	// EventClientToolsChanged tells every stream the set of registered client
	// tools changed (or is being re-announced).
	EventClientToolsChanged EventType = "client_tools.changed"
	// EventClientToolRequest announces a parked client-tool call. Provider
	// streams get the whole request, other streams only its id.
	EventClientToolRequest EventType = "client_tool.request"
	// EventClientToolCancelled announces that a parked call ended without a
	// result, so a provider can abort its work. Same redaction as the request.
	EventClientToolCancelled EventType = "client_tool.cancelled"
)

// Reasons a parked client-tool call was cancelled.
const (
	CancelTimeout   = "timeout"
	CancelCancelled = "cancelled"
	CancelAgentGone = "agent_gone"
)

// ClientToolsChangedPayload carries the registry revision and the names of
// every registered tool.
type ClientToolsChangedPayload struct {
	Meta
	Revision uint64   `json:"revision"`
	Tools    []string `json:"tools"`
}

// ClientToolRequest is one parked call: what the owning app is asked to run.
// Owner is the client that owns the agent's group when it was published.
type ClientToolRequest struct {
	ID        string          `json:"id"`
	Agent     string          `json:"agent"`
	Group     string          `json:"group"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Owner     string          `json:"owner"`
	TimeoutS  int             `json:"timeout_s"`
	CreatedAt time.Time       `json:"created_at"`
}

// ClientToolRequestPayload is a client_tool.request for provider streams.
type ClientToolRequestPayload struct {
	Meta
	Request ClientToolRequest `json:"request"`
}

// ClientToolRequestRedactedID is the request as non-provider streams see it.
type ClientToolRequestRedactedID struct {
	ID string `json:"id"`
}

// ClientToolRequestRedacted is a client_tool.request for non-provider
// streams: it keeps the stream's seq contiguous without leaking the call.
type ClientToolRequestRedacted struct {
	Meta
	Request ClientToolRequestRedactedID `json:"request"`
}

// Redacted returns the payload as non-provider streams see it.
func (p ClientToolRequestPayload) Redacted() ClientToolRequestRedacted {
	return ClientToolRequestRedacted{Meta: p.Meta, Request: ClientToolRequestRedactedID{ID: p.Request.ID}}
}

// ClientToolCancelledPayload is a client_tool.cancelled for provider streams.
type ClientToolCancelledPayload struct {
	Meta
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// ClientToolCancelledRedacted is a client_tool.cancelled for non-provider
// streams.
type ClientToolCancelledRedacted struct {
	Meta
	ID string `json:"id"`
}

// Redacted returns the payload as non-provider streams see it.
func (p ClientToolCancelledPayload) Redacted() ClientToolCancelledRedacted {
	return ClientToolCancelledRedacted{Meta: p.Meta, ID: p.ID}
}
