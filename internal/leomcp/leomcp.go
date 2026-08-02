// Package leomcp manages the auto-injected MCP config that wires Leo's
// built-in MCP server into every supervised Claude process (and task run).
//
// The MCP server itself lives in internal/mcp; this package handles writing
// the small JSON config file Claude Code consumes via --mcp-config and
// generating the matching CLI flag.
package leomcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ConfigPath returns the on-disk path for the Leo-managed MCP config file.
// One file shared across all processes/tasks (the entry is identical;
// per-process scoping happens via env vars at spawn).
func ConfigPath(cfg *config.Config) string {
	return filepath.Join(cfg.StatePath(), "leo-mcp.json")
}

// EnsureConfig writes the Leo-managed MCP config file if it isn't already
// up to date. Idempotent. Returns the absolute path on success.
func EnsureConfig(cfg *config.Config) (string, error) {
	path := ConfigPath(cfg)
	want := buildConfig()

	if existing, err := os.ReadFile(path); err == nil && bytesEqual(existing, want) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return "", fmt.Errorf("write leo MCP config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename leo MCP config: %w", err)
	}
	return path, nil
}

// AppendArg appends `--mcp-config <path>` to args, always wiring the leo MCP
// server into the supervised Claude process. When the daemon's TCP listener
// is enabled the server operates in full mode (daemon-backed tools like
// leo_send_message); without it, the server self-selects local-only mode
// from its environment and serves only the local leo_skill tool.
func AppendArg(args []string, cfg *config.Config) []string {
	if cfg == nil {
		return args
	}
	if cfg.HomePath == "" {
		// No valid state dir to write into (HomePath is always set in real
		// daemon/CLI paths); skip rather than writing leo-mcp.json relative
		// to the process's cwd, which pollutes whatever directory tests or
		// misconfigured callers happen to run from.
		return args
	}
	path, err := EnsureConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leo: warning: could not write leo MCP config: %v\n", err)
		return args
	}
	return append(args, "--mcp-config", path)
}

// leoSkillNudgeText tells a coding agent it can call the leo_skill tool for
// step-by-step instructions on operating Leo itself (scheduling/triggering
// tasks, reading logs, managing the daemon and agents). The leo_skill tool
// is always available — the MCP server is wired in regardless of web mode —
// so this text is unconditional.
const leoSkillNudgeText = "When you need to operate Leo — schedule or trigger tasks, read logs, or manage the daemon and agents — call the `leo_skill` tool for step-by-step instructions."

// leoMessagingNudgeText tells a coding agent it can message other Leo
// agents/the user. Only true when the daemon's web listener is enabled (the
// daemon-backed messaging tools require it), so it's gated on
// cfg.Web.Enabled.
const leoMessagingNudgeText = "You're running under Leo. Message other agents or the user with the `leo_send_message` tool (use `leo_list_agents` to see who's running)."

// leoConsultNudgeText disambiguates the two tools operators most often
// conflate. "Consult fable" names a template (a model), not a running agent,
// so without this the nearest-neighbour choice is leo_send_message aimed at
// whichever agent happens to look similar. Daemon-backed like messaging, so
// it ships under the same cfg.Web.Enabled gate.
const leoConsultNudgeText = "When asked to consult, ask, or get a second opinion from another model by name (\"consult fable\", \"ask codex\", \"what does opus think\"), use the `leo_consult` tool with that name as its `template` — those names come from `leo_list_templates` and are models, not running agents, so `leo_send_message` is the wrong tool for them."

// LeoNudge returns Leo's built-in harness-neutral system-prompt addendum.
// The leo_skill guidance is always included since the leo_skill tool is
// always available; the messaging and consult guidance is included only when
// the daemon's web listener is enabled (cfg.Web.Enabled), since that's what
// those daemon-backed tools require. Returns "" only when cfg is nil.
// Callers set harness.LaunchSpec.SystemContext to this value; each adapter
// renders it via its own native channel.
func LeoNudge(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Web.Enabled {
		return leoMessagingNudgeText + " " + leoConsultNudgeText + " " + leoSkillNudgeText
	}
	return leoSkillNudgeText
}

// MergeSystemPrompt combines Leo's built-in nudge with any user-configured
// prompt into a single value, nudge first. Returns "" when there is nothing
// to append. Kept for callers that render a single merged string rather than
// threading LaunchSpec.SystemContext through an adapter.
func MergeSystemPrompt(cfg *config.Config, userPrompt string) string {
	builtin := LeoNudge(cfg)
	switch {
	case builtin == "":
		return userPrompt
	case userPrompt == "":
		return builtin
	default:
		return builtin + "\n\n" + userPrompt
	}
}

func buildConfig() []byte {
	v := map[string]any{
		"mcpServers": map[string]any{
			"leo": map[string]any{
				"command": "leo",
				"args":    []string{"mcp-server"},
			},
		},
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Fall back to a guaranteed-valid hand-rolled string.
		return []byte(`{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}` + "\n")
	}
	return append(out, '\n')
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
