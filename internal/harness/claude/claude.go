// Package claude adapts leo's harness-neutral LaunchSpec to the Claude Code
// CLI. Flag order per Kind is load-bearing: it reproduces the pre-harness
// arg builders byte-for-byte so the characterization tests hold.
package claude

import (
	"fmt"
	"sort"

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

// validModels is the hardcoded Claude Code model list, moved here from
// internal/config so model policy lives with the adapter.
var validModels = map[string]bool{
	"sonnet": true, "opus": true, "haiku": true,
	"sonnet[1m]": true, "opus[1m]": true,
}

// ValidModels returns the accepted model names, sorted.
func ValidModels() []string {
	names := make([]string, 0, len(validModels))
	for name := range validModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (Claude) Name() string   { return "claude" }
func (Claude) Binary() string { return "claude" }

// ValidateModel reports whether model is acceptable for Claude Code. Empty
// string is always valid (harness default).
func (Claude) ValidateModel(model string) error {
	if model == "" || validModels[model] {
		return nil
	}
	return fmt.Errorf("%q is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])", model)
}

// SupportsChannels reports that Claude Code hosts channel plugins.
func (Claude) SupportsChannels() bool { return true }

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
