package agent

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// BuildTemplateArgs assembles the claude CLI arguments for an agent spawned from a template.
// The override cascade is template → defaults → built-in default.
func BuildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace string) []string {
	var args []string

	model := tmpl.Model
	if model == "" {
		model = cfg.Defaults.Model
	}
	if model == "" {
		model = config.DefaultModel
	}
	args = append(args, "--model", model)

	for _, ch := range tmpl.Channels {
		args = append(args, "--channels", ch)
	}

	args = append(args, "--add-dir", workspace)
	for _, dir := range tmpl.AddDirs {
		args = append(args, "--add-dir", dir)
	}

	rc := true
	if tmpl.RemoteControl != nil {
		rc = *tmpl.RemoteControl
	}
	if rc {
		args = append(args, "--remote-control")
	}

	args = append(args, "--name", agentName)

	permMode := tmpl.PermissionMode
	if permMode == "" {
		permMode = cfg.Defaults.PermissionMode
	}
	if permMode != "" {
		args = append(args, "--permission-mode", permMode)
	}

	if tmpl.MCPConfig != "" {
		mcpPath := tmpl.MCPConfig
		if !filepath.IsAbs(mcpPath) {
			mcpPath = filepath.Join(workspace, mcpPath)
		}
		if config.HasMCPServers(mcpPath) {
			args = append(args, "--mcp-config", mcpPath)
		}
	}

	if tmpl.Agent != "" {
		args = append(args, "--agent", tmpl.Agent)
	}

	allowed := tmpl.AllowedTools
	if len(allowed) == 0 {
		allowed = cfg.Defaults.AllowedTools
	}
	if len(allowed) > 0 {
		args = append(args, "--allowed-tools", strings.Join(allowed, ","))
	}

	disallowed := tmpl.DisallowedTools
	if len(disallowed) == 0 {
		disallowed = cfg.Defaults.DisallowedTools
	}
	if len(disallowed) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(disallowed, ","))
	}

	appendPrompt := tmpl.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}

	maxTurns := tmpl.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.Defaults.MaxTurns
	}
	if maxTurns == 0 {
		maxTurns = config.DefaultMaxTurns
	}
	args = append(args, "--max-turns", strconv.Itoa(maxTurns))

	return args
}
