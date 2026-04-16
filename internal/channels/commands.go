// Package channels defines the canonical slash-command vocabulary that
// every Leo channel plugin gets "for free" via the leo MCP server.
//
// The list here is the single source of truth consumed by:
//   - internal/mcp — exposes one MCP tool per command for the supervised Claude
//   - internal/cli (channels register-commands telegram) — registers commands
//     with the Telegram Bot API for autocomplete
//   - internal/templates/claude-workspace.md — skill text the supervised Claude
//     reads to learn the command vocabulary; a unit test asserts every name
//     here appears in that skill so drift is caught at build time
package channels

// Command is a user-facing slash command available in every channel plugin.
type Command struct {
	// Name is what the user types after the leading "/" — lowercase, no slash.
	Name string
	// Description is the one-line explanation shown in autocomplete UIs and
	// in the skill text presented to the supervised Claude.
	Description string
}

// Canonical is the full set of slash commands Leo recognizes across every
// channel. Adding to this list is a contract change — update the MCP server
// tool list and the claude-workspace.md skill in lockstep.
var Canonical = []Command{
	{Name: "clear", Description: "Clear conversation context"},
	{Name: "compact", Description: "Compact conversation context"},
	{Name: "stop", Description: "Interrupt current operation"},
	{Name: "tasks", Description: "List scheduled tasks"},
	{Name: "agent", Description: "Spawn an ephemeral agent"},
	{Name: "agents", Description: "List running ephemeral agents"},
}
