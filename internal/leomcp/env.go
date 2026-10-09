package leomcp

import (
	"encoding/json"
	"log"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/config"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
)

// Environment variables `leo mcp-server` (internal/mcp) reads to bind itself
// to a process and authenticate against the daemon. Without EnvWebPort and
// EnvAPIToken the server degrades to local-only mode (leo_skill only).
const (
	EnvProcessName = "LEO_PROCESS_NAME"
	EnvWebPort     = "LEO_WEB_PORT"
	EnvAPIToken    = "LEO_API_TOKEN"
	EnvDispatchID  = "LEO_DISPATCH_ID"
	EnvPermissions = "LEO_PERMISSIONS"
)

// DispatchProcessPrefix prefixes the LEO_PROCESS_NAME of a dispatch child.
// The name deliberately matches no supervised agent, so tools keyed on it
// (leo_clear, leo_interrupt, leo_send_message's sender) can never act on, or
// speak as, the root agent that started the dispatch. Attribution of nested
// dispatches rides EnvDispatchID instead (the daemon resolves the root caller
// from the parent dispatch record).
const DispatchProcessPrefix = "dispatch:"

// BridgeEnvNames returns the env-var *names* a codex bridge forwards into the
// leo MCP server (codex forwards by name, so a variable missing here never
// reaches the server). EnvDispatchID is always named: codex skips a named
// variable that is unset, so supervised agents are unaffected, and a codex run
// inside a dispatch needs it for the dispatch-scoped tool restrictions.
func BridgeEnvNames(restricted bool) []string {
	names := []string{EnvProcessName, EnvWebPort, EnvAPIToken, EnvDispatchID}
	if restricted {
		names = append(names, EnvPermissions)
	}
	return names
}

// PermissionsEnv renders tmpl's permissions as the single-entry env overlay
// the leo MCP server reads at startup, or nil when the template places no
// restriction.
func PermissionsEnv(tmpl config.TemplateConfig) map[string]string {
	if tmpl.Permissions.IsZero() {
		return nil
	}
	payload, err := json.Marshal(tmpl.Permissions)
	if err != nil {
		// Unreachable for a struct of string slices, but failing open would
		// silently hand the agent the full tool surface. Drop the overlay and
		// say so; the MCP server then runs unrestricted, which the log makes
		// visible rather than silent.
		log.Printf("[leomcp] marshaling permissions: %v", err)
		return nil
	}
	return map[string]string{EnvPermissions: string(payload)}
}

// DispatchChildEnv is the leo MCP environment of a leo_dispatch subagent with
// the given dispatch id: its own attributable process name, the dispatch id
// (which scopes the server's dispatch-only restrictions and parents nested
// dispatches), the template's permissions, and — only when the daemon's web
// listener is enabled and token is non-empty — the port and token that put the
// server in full mode. Otherwise the child stays local-only, as before.
//
// The token is a secret: callers must deliver it through process environment
// or a file, never argv (see the interactive runtime).
func DispatchChildEnv(cfg *config.Config, tmpl config.TemplateConfig, id, token string) map[string]string {
	env := map[string]string{
		EnvProcessName: DispatchProcessPrefix + id,
		EnvDispatchID:  id,
	}
	for k, v := range PermissionsEnv(tmpl) {
		env[k] = v
	}
	if cfg != nil && cfg.Web.Enabled && token != "" {
		env[EnvWebPort] = strconv.Itoa(cfg.WebPort())
		env[EnvAPIToken] = token
	}
	return env
}

// CodexBridge is the codex harness's leo MCP bridge: the server command plus
// the by-name env whitelist (values stay out of ps-visible argv).
func (s Server) CodexBridge(restricted bool) *codexharness.LeoMCPBridge {
	return &codexharness.LeoMCPBridge{
		Command:      s.Executable(),
		Args:         s.Args(),
		EnvVars:      BridgeEnvNames(restricted),
		ApprovalMode: "approve",
		ToolTimeout:  ToolTimeout,
	}
}

// OpencodeBridge is the opencode harness's leo MCP bridge. opencode carries
// the server's environment inside its per-spawn config, so env travels as part
// of the launch env overlay, never argv.
func (s Server) OpencodeBridge(env map[string]string) *opencodeharness.LeoMCPBridge {
	return &opencodeharness.LeoMCPBridge{Command: s.Command(), Env: env, ToolTimeout: ToolTimeout}
}
