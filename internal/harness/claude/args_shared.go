package claude

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// strictMCPArgs is the strict MCP flags for a MCP "none" run. A config that
// may carry secrets (Private) is passed by file path only; without a file it
// fails closed instead of inlining it into argv.
func strictMCPArgs(o Options) ([]string, error) {
	if o.MCP != "none" {
		return nil, nil
	}
	if o.StrictMCPConfigFile != "" {
		return []string{"--strict-mcp-config", "--mcp-config", o.StrictMCPConfigFile}, nil
	}
	if o.StrictMCPConfigPrivate {
		return nil, errors.New("claude: private MCP config has no file; refusing to put it in argv")
	}
	config := o.StrictMCPConfig
	if config == "" {
		config = emptyStrictMCPConfig
	}
	return []string{"--strict-mcp-config", "--mcp-config", config}, nil
}

func settingsJSON(base map[string]any, o Options) string {
	if o.Plugins == "none" {
		ids := append([]string(nil), o.EnabledPlugins...)
		sort.Strings(ids)
		enabled := make(map[string]bool, len(ids))
		for _, id := range ids {
			enabled[id] = false
		}
		base["enabledPlugins"] = enabled
	}
	b, _ := json.Marshal(base)
	return string(b)
}

func appendChannelFlags(args []string, channels, devChannels []string) []string {
	for _, ch := range channels {
		args = append(args, "--channels", ch)
	}
	for _, ch := range devChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	return args
}

func appendPermissionFlags(args []string, o Options) []string {
	if o.PermissionMode != "" {
		return append(args, "--permission-mode", o.PermissionMode)
	}
	if o.BypassPermissions {
		return append(args, "--dangerously-skip-permissions")
	}
	return args
}

func appendToolFlags(args []string, o Options) []string {
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(o.DisallowedTools, ","))
	}
	return args
}
