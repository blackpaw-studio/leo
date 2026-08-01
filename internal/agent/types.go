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
	// Adopt requests that the supervisor re-attach to an already-running tmux
	// session for this agent instead of killing and recreating it. Set by
	// RestoreAgents when an agent's session survived a daemon bounce (e.g.
	// `leo update` / `leo service restart` SIGKILL the daemon but leave the
	// independent tmux sessions running) so restarting the daemon no longer
	// disrupts in-flight agents. Only honored for the first supervise
	// iteration; a later in-loop restart spawns a fresh session as usual.
	Adopt bool
	// Harness is the resolved harness adapter name (e.g. "claude", "codex").
	// Empty means "claude" — the value predates this field on records/specs
	// written before it existed.
	Harness string
	// OpeningPrompt carries the agent's opening turn for harnesses whose
	// driver injects it into the TUI pane after launch (the tmux-TUI driver's
	// Start call) rather than passing it as a trailing positional claude arg.
	// Empty for claude, which keeps the prompt in ClaudeArgs.
	OpeningPrompt string
	// Resumed is set by Manager.Resume to mark this spawn as reviving a
	// suspended agent rather than creating a new one. The supervisor uses it
	// to announce the transition as observe.EventAgentStateChanged (status
	// "starting") instead of observe.EventAgentSpawned — a consumer that saw
	// this agent suspend already knows about it and needs a state
	// transition, not a "new agent appeared" event.
	Resumed bool
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
