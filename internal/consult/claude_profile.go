package consult

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// resolveClaudeDispatchProfile applies lean defaults only when neither scope
// explicitly opts into a profile. It returns a copy and never mutates decoded.
func resolveClaudeDispatchProfile(cfg *config.Config, tmpl config.TemplateConfig, kind string, o claudeharness.Options, env map[string]string, mcp leomcp.Server) claudeharness.Options {
	if kind != "dispatch" {
		return o
	}
	if len(o.LeoMCPArgs) == 0 {
		o.LeoMCPArgs = mcp.AppendArg(nil, cfg)
	}
	if len(o.LeoMCPArgs) > 0 {
		o.LeoMCPToolTimeout = leomcp.ToolTimeout
	}
	useDefaults := cfg.DefaultsHarness() == cfg.TemplateHarness(tmpl)
	_, defaultMCP := cfg.Defaults.HarnessOptions["mcp"]
	defaultMCP = defaultMCP && useDefaults
	_, templateMCP := tmpl.HarnessOptions["mcp"]
	if !defaultMCP && !templateMCP {
		o.MCP = "none"
	}
	_, defaultPlugins := cfg.Defaults.HarnessOptions["plugins"]
	defaultPlugins = defaultPlugins && useDefaults
	_, templatePlugins := tmpl.HarnessOptions["plugins"]
	if !defaultPlugins && !templatePlugins {
		o.Plugins = "none"
	}
	if o.MCP == "none" {
		bridgePresent := len(o.LeoMCPArgs) > 0
		o.MCPConfigPath = ""
		o.LeoMCPArgs = nil
		if bridgePresent {
			o.StrictMCPConfig = mcp.InlineConfig()
		} else {
			o.StrictMCPConfig = `{"mcpServers":{}}`
		}
	}
	if o.Plugins == "none" {
		o.EnabledPlugins = installedClaudePlugins(env)
	}
	return o
}

var installedClaudePlugins = claudeharness.InstalledPluginIDs

// resolveClaudeDispatchOptions is resolveClaudeDispatchProfile plus the
// template's mcp_servers and the caller's additional list, resolved from the
// launch env's account config. The warnings describe names that could not be
// added; they never carry server definitions. It applies to every kind: only a
// "none" profile can use them. A nil additional leaves the result exactly as
// the template alone would produce it.
func resolveClaudeDispatchOptions(cfg *config.Config, tmpl config.TemplateConfig, kind string, o claudeharness.Options, env map[string]string, mcp leomcp.Server, additional []string) (claudeharness.Options, []string) {
	o = resolveClaudeDispatchProfile(cfg, tmpl, kind, o, env, mcp)
	var extraWarnings []string
	if len(additional) > 0 {
		switch o.MCP {
		case "none":
			o.MCPServers = mergeServerNames(o.MCPServers, additional)
		default:
			// "inherit", or an explicit empty mcp, which loads the same
			// user-scope servers: there is nothing to add to.
			extraWarnings = []string{"additional_mcp_servers ignored under mcp: inherit (inherit already loads every user-scope server)"}
		}
	}
	o, warnings := claudeharness.ApplyMCPServers(o, env)
	return o, append(warnings, extraWarnings...)
}

// mergeServerNames returns base then extra with duplicates removed, in a new
// slice so the template's own list is never aliased or mutated.
func mergeServerNames(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, n := range append(append([]string(nil), base...), extra...) {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// withoutPrivateMCP drops the private-config marker from decoded claude
// options. An interactive Start only validates argv (the launch rebuilds and
// materializes its own), so it must not trip the fail-closed check.
func withoutPrivateMCP(decoded any) any {
	if o, ok := decoded.(claudeharness.Options); ok {
		o.StrictMCPConfigPrivate = false
		return o
	}
	return decoded
}

// logDispatchWarnings prints a run's launch warnings to the daemon log.
func logDispatchWarnings(id, template string, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "dispatch %s (template %s): warning: %s\n", id, template, w)
	}
}
