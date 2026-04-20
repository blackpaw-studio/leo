package agent

import "time"

// SpawnRequest is the minimal info needed for the Supervisor to start a new
// ephemeral agent. It lives here (not in daemon) so the agent package can
// define its Supervisor interface without an import cycle.
type SpawnRequest struct {
	Name       string
	ClaudeArgs []string
	WorkDir    string
	Env        map[string]string
	WebPort    string
	// WebToken is the daemon's API bearer token. The supervisor exports it
	// as LEO_API_TOKEN so the MCP server inside claude can authenticate
	// against the daemon's /api/* and /web/* routes.
	WebToken string
}

// ProcessState is the live supervisor view of a single agent/process.
// Mirrored by daemon and web as API DTOs.
type ProcessState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
	Ephemeral bool      `json:"ephemeral,omitempty"`
}
