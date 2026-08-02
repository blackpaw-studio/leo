// Package claude adapts leo's harness-neutral LaunchSpec to the Claude Code
// CLI. Flag order per Kind is load-bearing: it reproduces the pre-harness
// arg builders byte-for-byte so the characterization tests hold.
package claude

import (
	"fmt"
	"strconv"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/tmuxtui"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// Options carries the claude-specific knobs, fully resolved by the caller:
// cascades applied, system prompt merged (leomcp.MergeSystemPrompt), MCP
// paths gated (config.HasMCPServers), leo MCP flag precomputed.
type Options struct {
	PermissionMode     string
	BypassPermissions  bool // legacy fallback; only consulted when PermissionMode == ""
	RemoteControl      bool
	AgentFile          string // --agent
	AllowedTools       []string
	DisallowedTools    []string
	AppendSystemPrompt string
	MCPConfigPath      string   // user MCP config; empty when absent or serverless
	LeoMCPArgs         []string // precomputed leomcp.AppendArg(nil, cfg); nil when gated off
	// LeoMCPToolTimeout is leo's per-tool MCP ceiling (leomcp.ToolTimeout).
	// Applied only when the leo bridge is actually wired (LeoMCPArgs
	// non-empty), since Claude Code's knob is process-global.
	LeoMCPToolTimeout time.Duration
}

// Claude is the Claude Code adapter.
type Claude struct{}

func init() { harness.Register(Claude{}) }

// suggestedModels seeds the web UI's model datalist. It is a convenience
// hint, NOT an allowlist: `claude --model` also takes aliases released after
// any given leo build, full model IDs ("claude-fable-5"), and whatever a
// third-party ANTHROPIC_BASE_URL endpoint calls its models. Keep it sorted.
var suggestedModels = []string{
	"fable", "haiku", "opus", "opus[1m]", "sonnet", "sonnet[1m]",
}

// SuggestedModels returns the datalist hints for the model input, sorted.
func SuggestedModels() []string {
	return append([]string(nil), suggestedModels...)
}

func (Claude) Name() string   { return "claude" }
func (Claude) Binary() string { return "claude" }

// ValidateModel is a format check only. Claude Code resolves model names
// itself — aliases ("sonnet", "fable"), full IDs ("claude-fable-5"), and
// whatever a third-party ANTHROPIC_BASE_URL endpoint serves — and reports an
// unknown one at launch. An allowlist here would just reject models released
// after the leo build the user happens to be running.
func (Claude) ValidateModel(model string) error {
	return harness.ValidateModelFormat(model)
}

// SupportsChannels reports that Claude Code hosts channel plugins.
func (Claude) SupportsChannels() bool { return true }

// Env returns claude-specific spawn env. One-shot task runs set the CLI
// entrypoint marker (moved here from the task runner); interactive kinds
// export their env at tmux launch instead. A wired leo MCP bridge also
// raises MCP_TOOL_TIMEOUT to leo's own ceiling, so a long leo_consult isn't
// cut short by Claude Code's default per-tool deadline. That knob has no
// per-server form, so it lifts the ceiling for every MCP server in the
// process — hence the gate on the bridge actually being present.
func (Claude) Env(spec harness.LaunchSpec) (map[string]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("claude: spec.Options is %T, want claude.Options", spec.Options)
	}
	env := map[string]string{}
	if spec.Kind == harness.KindTask {
		env["CLAUDE_CODE_ENTRYPOINT"] = "cli"
	}
	if len(opts.LeoMCPArgs) > 0 && opts.LeoMCPToolTimeout > 0 {
		env["MCP_TOOL_TIMEOUT"] = strconv.FormatInt(opts.LeoMCPToolTimeout.Milliseconds(), 10)
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

// SupportsKind: claude runs every leo primitive.
func (Claude) SupportsKind(harness.Kind) bool { return true }

// Driver: the shared tmux-TUI driver with claude's probe profile, dialog
// policy, and --session-id → --resume → fresh quick-exit ladder.
func (Claude) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:     tmux.ClaudeProfile(),
		PaneKeyFn: DialogKey,
		RecoverFn: RecoverQuickExitArgs,
	})
}

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
	case harness.KindAgent:
		return agentArgs(spec, opts), nil
	case harness.KindTask:
		return taskArgs(spec, opts), nil
	default:
		return nil, fmt.Errorf("claude: unknown launch kind %q", spec.Kind)
	}
}
