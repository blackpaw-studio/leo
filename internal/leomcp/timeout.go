package leomcp

import (
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// ToolTimeout is the ceiling leo hands each coding agent for a single leo MCP
// tool call. Every harness ships its own, much shorter, per-tool MCP deadline
// (codex kills a call at a few minutes), which would truncate leo_consult —
// a full agent run — long before leo's own deadline expires, replacing leo's
// structured timeout error with a generic harness one.
//
// It sits above consult.RunTimeout (the authoritative deadline) plus the MCP
// client's HTTP margin, so leo always wins the race and reports the timeout
// itself. Callers wiring the leo MCP bridge pass this to the adapter, which
// renders it through its own native knob (codex `tool_timeout_sec`, opencode
// `mcp.leo.timeout`, claude `MCP_TOOL_TIMEOUT`).
const ToolTimeout = consult.RunTimeout + 2*time.Minute
