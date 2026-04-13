package daemon

import "encoding/json"

// Response is the standard envelope for all daemon API responses.
// Code is an optional machine-readable classifier for failures (e.g.
// "not_found", "ambiguous") so clients can reconstruct typed errors without
// string-matching. Matches is populated alongside Code="ambiguous".
type Response struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"`
	Matches []string        `json:"matches,omitempty"`
}

// Error code constants used on the wire.
const (
	ErrorCodeNotFound  = "not_found"
	ErrorCodeAmbiguous = "ambiguous"
)

// TaskAddRequest is the body for POST /task/add.
type TaskAddRequest struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	PromptFile string `json:"prompt_file"`
	Model      string `json:"model,omitempty"`
	MaxTurns   int    `json:"max_turns,omitempty"`
	TopicID    int    `json:"topic_id,omitempty"`
	Silent     bool   `json:"silent,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// TaskNameRequest is the body for POST /task/remove, /task/enable, /task/disable.
type TaskNameRequest struct {
	Name string `json:"name"`
}

// AgentSpawnRequest is the body for POST /agents/spawn.
type AgentSpawnRequest struct {
	Template string `json:"template"`
	Repo     string `json:"repo"`
	Name     string `json:"name,omitempty"`
}

// AgentLogsResponse is the payload for GET /agents/{name}/logs.
type AgentLogsResponse struct {
	Output string `json:"output"`
}

// AgentSessionResponse is the payload for GET /agents/{name}/session.
// Name is the canonical agent name the query resolved to; may differ from the
// request path when the server accepts shorthand. Always populated so clients
// can distinguish "resolved to empty" from "field not sent by old server".
type AgentSessionResponse struct {
	Session string `json:"session"`
	Name    string `json:"name"`
}

// AgentResolveResponse is the payload for GET /agents/resolve?q=<query>.
type AgentResolveResponse struct {
	Name    string `json:"name"`
	Session string `json:"session"`
	Repo    string `json:"repo,omitempty"`
}
