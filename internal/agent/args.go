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

	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		log.Printf("[agent:%s] resolving harness: %v", agentName, err)
		return nil
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		log.Printf("[agent:%s] decoding harness options: %v", agentName, err)
		return nil
	}
	opts, ok := decoded.(claudeharness.Options)
	if !ok {
		// Non-claude templates arrive with Plan 4 (session drivers).
		log.Printf("[agent:%s] harness %q cannot spawn agents yet", agentName, h.Name())
		return nil
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

	// Agents default remote_control to true, and only the template's own
	// options can turn it off — the defaults layer never applied to
	// templates pre-migration and still doesn't (see plan: preserved quirks).
	opts.RemoteControl = true
	if v, ok := tmpl.HarnessOptions["remote_control"].(bool); ok {
		opts.RemoteControl = v
	}
	opts.AppendSystemPrompt = leomcp.MergeSystemPrompt(cfg, opts.AppendSystemPrompt)
	opts.MCPConfigPath = mcpConfig
	opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)

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
		Options:     opts,
	}
	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[agent:%s] building %s args: %v", agentName, h.Name(), err)
		return nil
	}
	return args
}
