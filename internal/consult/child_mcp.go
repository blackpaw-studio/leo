package consult

import (
	"github.com/blackpaw-studio/leo/internal/config"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// withLeoMCPBridge wires the leo MCP server into a dispatch child's harness
// options the way supervised agents get it. Claude already carries its bridge
// via resolveClaudeDispatchProfile; codex and opencode have no bridge until
// this adds one. env is the child's leo MCP environment (opencode embeds it in
// its launch config; codex forwards variables by name from the process env).
func withLeoMCPBridge(decoded any, mcp leomcp.Server, tmpl config.TemplateConfig, env map[string]string) any {
	switch opts := decoded.(type) {
	case codexharness.Options:
		opts.LeoMCP = mcp.CodexBridge(!tmpl.Permissions.IsZero())
		return opts
	case opencodeharness.Options:
		copied := make(map[string]string, len(env))
		for k, v := range env {
			copied[k] = v
		}
		opts.LeoMCP = mcp.OpencodeBridge(copied)
		return opts
	}
	return decoded
}

// headlessChildMCP returns the harness options with the leo MCP bridge wired
// in and the leo-owned env overlay for dispatch id's headless child. The
// overlay is applied last at launch so neither a template env nor an env the
// daemon inherited can mask the child's identity or credentials.
func (d *Dispatcher) headlessChildMCP(cfg *config.Config, tmpl config.TemplateConfig, decoded any, id string) (any, map[string]string) {
	env := leomcp.DispatchChildEnv(cfg, tmpl, id)
	if cfg != nil && cfg.Web.Enabled && d.AgentToken != "" {
		env[leomcp.EnvAPIToken] = d.AgentToken
	}
	return withLeoMCPBridge(decoded, d.LeoMCP, tmpl, env), env
}
