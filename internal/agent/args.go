package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// resolveTemplateLaunch resolves the config cascade for a template spawn into
// a harness.Harness + fully-populated harness.LaunchSpec, stopping just short
// of calling h.Args(spec). Split out from BuildTemplateArgs so tests can
// assert on spec.Options (e.g. a codex/opencode LeoMCP bridge) without
// needing Args() to succeed.
//
// webToken is the daemon's API bearer token (Manager.webToken). The
// non-claude LeoMCP bridge is always wired in (mirrors run/runner.go's
// leoMCPEnv and cli's processLeoMCPEnv); webToken/cfg.WebPort() may be empty
// or zero when web is disabled, in which case the leo MCP server self-selects
// local-only mode at runtime (only leo_skill is served).
func resolveTemplateLaunch(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace, prompt, webToken string) (harness.Harness, harness.LaunchSpec, error) {
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
		return nil, harness.LaunchSpec{}, fmt.Errorf("resolving harness: %w", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return nil, harness.LaunchSpec{}, fmt.Errorf("decoding harness options: %w", err)
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
		Kind:          harness.KindAgent,
		Name:          agentName,
		Model:         cfg.TemplateModel(tmpl),
		MaxTurns:      maxTurns,
		Workspace:     workspace,
		AddDirs:       safeDirs,
		Channels:      tmpl.Channels,
		DevChannels:   tmpl.DevChannels,
		Prompt:        prompt,
		SystemContext: leomcp.LeoNudge(cfg),
	}

	switch opts := decoded.(type) {
	case claudeharness.Options:
		// Agents default remote_control to true, and only the template's own
		// options can turn it off — the defaults layer never applied to
		// templates pre-migration and still doesn't (see plan: preserved quirks).
		opts.RemoteControl = true
		if v, ok := tmpl.HarnessOptions["remote_control"].(bool); ok {
			opts.RemoteControl = v
		}
		opts.MCPConfigPath = mcpConfig
		opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)
		opts.LeoMCPToolTimeout = leomcp.ToolTimeout
		spec.Options = opts
	case codexharness.Options:
		opts.LeoMCP = &codexharness.LeoMCPBridge{
			Command:      "leo",
			Args:         []string{"mcp-server"},
			EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
			ApprovalMode: "approve",
			ToolTimeout:  leomcp.ToolTimeout,
		}
		spec.Options = opts
	case opencodeharness.Options:
		opts.LeoMCP = &opencodeharness.LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env: map[string]string{
				"LEO_PROCESS_NAME": agentName,
				"LEO_WEB_PORT":     strconv.Itoa(cfg.WebPort()),
				"LEO_API_TOKEN":    webToken,
			},
			ToolTimeout: leomcp.ToolTimeout,
		}
		spec.Options = opts
	default:
		return h, harness.LaunchSpec{}, fmt.Errorf("harness %q returned unsupported options type %T", h.Name(), decoded)
	}

	return h, spec, nil
}

// BuildTemplateArgs assembles the CLI arguments for an agent spawned from a template.
// The override cascade is template → defaults → built-in default.
//
// When prompt is non-empty it is appended as the trailing positional argument
// (claude only — codex/opencode carry the opening prompt elsewhere, via their
// session driver's Start). Claude Code treats a bare positional (with no
// -p/--print) as the opening turn of an interactive session, so the agent
// processes the prompt and then stays alive in its tmux REPL — the same
// behavior an empty prompt has, plus a first turn. An empty prompt appends
// nothing, preserving the prior arg list exactly.
//
// webToken is the daemon's API bearer token (Manager.webToken); see
// resolveTemplateLaunch.
//
// The second return is the harness's env overlay (h.Env(spec)) — e.g.
// OPENCODE_CONFIG_CONTENT for opencode — meant to be merged as the BASE
// layer under the template's own env and any per-spawn env
// (mergeEnv(mergeEnv(harnessEnv, tmpl.Env), spec.Env)), so caller-provided
// env always wins on collision. Nil for claude/codex, whose bridges ride argv
// or env-var *names* the supervisor already exports.
func BuildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace, prompt, webToken string) ([]string, map[string]string) {
	h, spec, err := resolveTemplateLaunch(cfg, tmpl, agentName, workspace, prompt, webToken)
	if err != nil {
		log.Printf("[agent:%s] %v", agentName, err)
		return nil, nil
	}
	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[agent:%s] building %s args: %v", agentName, h.Name(), err)
		return nil, nil
	}
	env, err := h.Env(spec)
	if err != nil {
		log.Printf("[agent:%s] building %s env: %v", agentName, h.Name(), err)
		return args, nil
	}
	return args, env
}
