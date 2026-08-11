package agent

import (
	"encoding/json"
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
			EnvVars:      leoMCPEnvVars(tmpl),
			ApprovalMode: "approve",
			ToolTimeout:  leomcp.ToolTimeout,
		}
		spec.Options = opts
	case opencodeharness.Options:
		opts.LeoMCP = &opencodeharness.LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env: mergeEnv(map[string]string{
				"LEO_PROCESS_NAME": agentName,
				"LEO_WEB_PORT":     strconv.Itoa(cfg.WebPort()),
				"LEO_API_TOKEN":    webToken,
			}, permissionsEnv(tmpl)),
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
		return args, permissionsEnv(tmpl)
	}
	return args, mergeEnv(env, permissionsEnv(tmpl))
}

// permissionsEnvVar carries the template's permission set to the leo MCP
// server running inside the agent. Unset for unrestricted templates, so their
// environment is byte-for-byte what it was before permissions existed.
const permissionsEnvVar = "LEO_PERMISSIONS"

// permissionsEnv renders tmpl's permissions as the single-entry env overlay
// the leo MCP server reads at startup, or nil when the template places no
// restriction.
//
// This is the one injection seam for permissions: every spawn, resume, and
// restart path merges the env BuildTemplateArgs returns as its base layer, so
// folding the payload in here covers all of them — and means a restart
// re-resolves permissions from current config, which is how a config edit
// takes effect.
func permissionsEnv(tmpl config.TemplateConfig) map[string]string {
	if tmpl.Permissions.IsZero() {
		return nil
	}
	payload, err := json.Marshal(tmpl.Permissions)
	if err != nil {
		// Unreachable for a struct of string slices, but failing open would
		// silently hand the agent the full tool surface. Drop the overlay and
		// say so; the MCP server then runs unrestricted, which the log makes
		// visible rather than silent.
		log.Printf("[agent] marshaling permissions: %v", err)
		return nil
	}
	return map[string]string{permissionsEnvVar: string(payload)}
}

// applyPermissions returns a copy of env whose LEO_PERMISSIONS matches tmpl
// exactly: present when the template restricts, absent when it does not.
//
// Leo owns this variable — config is its only source. Env layers that persist
// across a restart (a record's stored Env, an inherited parent env, an
// explicit --env override) must never introduce or preserve it, or a
// restriction the operator removed would silently outlive the edit: the agent
// would keep running restricted while the config says otherwise. Normalizing
// after every merge makes that impossible regardless of layer order.
//
// This governs the env leo composes, not the environment the agent inherits
// from the process tree. An unrestricted template omits the key rather than
// clearing it, so a LEO_PERMISSIONS exported into a foreground `leo service`
// shell would reach every agent it starts and no restart would clear it. The
// supervised path is safe (env.Capture() is an allowlist), so this only bites
// while hand-running a daemon with the variable already exported — but if
// agents ever appear restricted with no config to match, look here first.
func applyPermissions(env map[string]string, tmpl config.TemplateConfig) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		if k == permissionsEnvVar {
			continue
		}
		out[k] = v
	}
	for k, v := range permissionsEnv(tmpl) {
		out[k] = v
	}
	return out
}

// leoMCPEnvVars returns the env-var *names* the codex bridge forwards into
// the leo MCP server. codex forwards by name rather than value, so a variable
// missing from this list never reaches the server no matter what the process
// environment holds.
func leoMCPEnvVars(tmpl config.TemplateConfig) []string {
	names := []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"}
	if !tmpl.Permissions.IsZero() {
		names = append(names, permissionsEnvVar)
	}
	return names
}
