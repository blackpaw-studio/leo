package consult

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// resolveClaudeDispatchProfile applies lean defaults only when neither scope
// explicitly opts into a profile. It returns a copy and never mutates decoded.
func resolveClaudeDispatchProfile(cfg *config.Config, tmpl config.TemplateConfig, kind string, o claudeharness.Options, env map[string]string) claudeharness.Options {
	if kind != "dispatch" {
		return o
	}
	if len(o.LeoMCPArgs) == 0 {
		o.LeoMCPArgs = leomcp.AppendArg(nil, cfg)
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
			o.StrictMCPConfig = leomcp.InlineConfig()
		} else {
			o.StrictMCPConfig = `{"mcpServers":{}}`
		}
	}
	if o.Plugins == "none" {
		home := env["HOME"]
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		o.EnabledPlugins = installedClaudePlugins(home)
	}
	return o
}

var installedClaudePlugins = claudeharness.InstalledPluginIDsFromHome
