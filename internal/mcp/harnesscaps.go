package mcp

// honorsToolListChanged reports whether the MCP client named clientName (the
// initialize request's clientInfo.name) re-lists its tools when the server
// sends notifications/tools/list_changed, and can then call a tool that did
// not exist when it connected.
//
// The answers come from scripts/harness-probe, which runs each installed
// harness interactively against a server that adds a tool mid-session; re-run
// it after a harness upgrade and update this table from results/. A client
// not listed here is treated as not honoring it: it gets the fixed fallback
// tools (leo_app_tools / leo_app_call), which work either way.
//
// Last probed 2026-10-26: Claude Code 2.1.310, opencode 1.18.18 and
// codex-cli 0.162.1 (codex never re-lists within a session).
func honorsToolListChanged(clientName string) bool {
	switch clientName {
	case "claude-code", "opencode":
		return true
	default: // "codex-mcp-client" and anything unknown
		return false
	}
}
