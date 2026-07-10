package agent

import (
	"log"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// BuildTemplateArgs assembles the claude CLI arguments for an agent spawned from a template.
// The override cascade is template → provider default_model → defaults → built-in default.
//
// When prompt is non-empty it is appended as the trailing positional argument.
// Claude Code treats a bare positional (with no -p/--print) as the opening turn
// of an interactive session, so the agent processes the prompt and then stays
// alive in its tmux REPL — the same behavior an empty prompt has, plus a first
// turn. An empty prompt appends nothing, preserving the prior arg list exactly.
func BuildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace, prompt string) []string {
	// Defense in depth: Config.Validate() also rejects these, but skip
	// anything unsafe here in case spawn-time receives an unvalidated
	// config. Log noisily so the silent drop isn't invisible.
	safeDirs := make([]string, 0, len(tmpl.AddDirs))
	for _, dir := range tmpl.AddDirs {
		if err := config.ValidateAddDir(dir); err != nil {
			log.Printf("[agent:%s] skipping unsafe add_dirs entry %q: %v", agentName, dir, err)
			continue
		}
		safeDirs = append(safeDirs, dir)
	}

	rc := true
	if tmpl.RemoteControl != nil {
		rc = *tmpl.RemoteControl
	}

	mcpConfig := ""
	if tmpl.MCPConfig != "" {
		p := tmpl.MCPConfig
		if !filepath.IsAbs(p) {
			p = filepath.Join(workspace, p)
		}
		if config.HasMCPServers(p) {
			mcpConfig = p
		}
	}

	maxTurns := tmpl.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.Defaults.MaxTurns
	}
	if maxTurns == 0 {
		maxTurns = config.DefaultMaxTurns
	}

	spec := harness.LaunchSpec{
		Kind:        harness.KindAgent,
		Name:        agentName,
		Model:       cfg.TemplateModel(tmpl),
		MaxTurns:    maxTurns,
		Workspace:   workspace,
		AddDirs:     safeDirs,
		Channels:    tmpl.Channels,
		DevChannels: tmpl.DevChannels,
		Prompt:      prompt,
		Options: claudeharness.Options{
			PermissionMode:     harness.FallbackString(tmpl.PermissionMode, cfg.Defaults.PermissionMode),
			RemoteControl:      rc,
			AgentFile:          tmpl.Agent,
			AllowedTools:       harness.FallbackSlice(tmpl.AllowedTools, cfg.Defaults.AllowedTools),
			DisallowedTools:    harness.FallbackSlice(tmpl.DisallowedTools, cfg.Defaults.DisallowedTools),
			AppendSystemPrompt: leomcp.MergeSystemPrompt(cfg, harness.FallbackString(tmpl.AppendSystemPrompt, cfg.Defaults.AppendSystemPrompt)),
			MCPConfigPath:      mcpConfig,
			LeoMCPArgs:         leomcp.AppendArg(nil, cfg),
		},
	}
	args, err := claudeharness.Claude{}.Args(spec)
	if err != nil {
		log.Printf("[agent:%s] building claude args: %v", agentName, err)
		return nil
	}
	return args
}
