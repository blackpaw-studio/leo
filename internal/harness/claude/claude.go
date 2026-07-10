// Package claude adapts leo's harness-neutral LaunchSpec to the Claude Code
// CLI. Flag order per Kind is load-bearing: it reproduces the pre-harness
// arg builders byte-for-byte so the characterization tests hold.
package claude

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Options carries the claude-specific knobs, fully resolved by the caller:
// cascades applied, system prompt merged (leomcp.MergeSystemPrompt), MCP
// paths gated (config.HasMCPServers), leo MCP flag precomputed.
type Options struct {
	PermissionMode      string
	BypassPermissions   bool // legacy fallback; only consulted when PermissionMode == ""
	RemoteControl       bool
	RemoteControlPrefix string // when set, adds --remote-control-session-name-prefix
	AgentFile           string // --agent
	AllowedTools        []string
	DisallowedTools     []string
	AppendSystemPrompt  string
	MCPConfigPath       string   // user MCP config; empty when absent or serverless
	LeoMCPArgs          []string // precomputed leomcp.AppendArg(nil, cfg); nil when gated off
}

// Claude is the Claude Code adapter.
type Claude struct{}

func init() { harness.Register(Claude{}) }

func (Claude) Name() string   { return "claude" }
func (Claude) Binary() string { return "claude" }

func (Claude) SessionArgs(s harness.SessionState) []string {
	switch s.Mode {
	case harness.SessionResume:
		return []string{"--resume", s.ID}
	case harness.SessionPinned:
		return []string{"--session-id", s.ID}
	default:
		return nil
	}
}

func (c Claude) Args(spec harness.LaunchSpec) ([]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("claude: spec.Options is %T, want claude.Options", spec.Options)
	}
	switch spec.Kind {
	case harness.KindProcess:
		return processArgs(spec, opts), nil
	case harness.KindAgent:
		return agentArgs(spec, opts), nil
	case harness.KindTask:
		return taskArgs(spec, opts), nil
	default:
		return nil, fmt.Errorf("claude: unknown launch kind %q", spec.Kind)
	}
}
