package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/redact"
	"github.com/spf13/cobra"
)

// templateOwnOptions decodes a template's OWN (unmerged) harness_options into
// claude Options for display purposes — i.e. what the template itself
// declares, not the effective/cascaded view. Decode errors are swallowed and
// yield zero Options: display code must never fail on a possibly-invalid
// literal view, Validate() is the sole authority on correctness.
func templateOwnOptions(tmpl config.TemplateConfig) claudeharness.Options {
	decoded, err := claudeharness.Claude{}.DecodeOptions(tmpl.HarnessOptions)
	if err != nil {
		return claudeharness.Options{}
	}
	opts, _ := decoded.(claudeharness.Options)
	return opts
}

// Testability seams — replaced in tests.
var (
	templateIsTTY  = defaultIsTTY
	templateStdin  *bufio.Reader // if set, used instead of os.Stdin by add/confirm prompts
	templateStdout = os.Stdout
)

// templateReader returns the reader used by interactive prompts. Tests can
// set templateStdin to stub input.
func templateReader() *bufio.Reader {
	if templateStdin != nil {
		return templateStdin
	}
	return bufio.NewReader(os.Stdin)
}

func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage ephemeral agent templates",
		Long: `Manage the reusable agent templates defined in leo.yaml. Templates are
blueprints for ephemeral agents spawned via 'leo agent spawn <template>'.
Each template captures workspace, channel plugins, model, permission mode,
and other defaults that inherit from the top-level 'defaults' block.`,
		Example: `  leo template list
  leo template show coding
  leo template show coding --resolved
  leo template add --name coding --model opus --agent dev
  leo template remove coding`,
	}
	cmd.AddCommand(
		newTemplateListCmd(),
		newTemplateShowCmd(),
		newTemplateAddCmd(),
		newTemplateRemoveCmd(),
	)
	return cmd
}

// templateListEntry is the shape emitted by `leo template list --json`.
// Fields mirror the table columns and stay stable so callers can script
// against them.
type templateListEntry struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

func newTemplateListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured templates",
		Long: `List every agent template defined in leo.yaml. The default output is a
human-readable table with name, model, agent, and workspace columns.
Pass --json for machine-readable output suitable for scripting.`,
		Example: `  leo template list
  leo template list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			names := make([]string, 0, len(cfg.Templates))
			for name := range cfg.Templates {
				names = append(names, name)
			}
			sort.Strings(names)

			if asJSON {
				entries := make([]templateListEntry, 0, len(names))
				for _, name := range names {
					tmpl := cfg.Templates[name]
					entries = append(entries, templateListEntry{
						Name:      name,
						Model:     tmpl.Model,
						Agent:     templateOwnOptions(tmpl).AgentFile,
						Workspace: tmpl.Workspace,
					})
				}
				enc := json.NewEncoder(templateStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(cfg.Templates) == 0 {
				info.Println("No templates configured.")
				return nil
			}

			fmt.Printf("  %-20s %-10s %-20s %s\n", "NAME", "MODEL", "AGENT", "WORKSPACE")
			for _, name := range names {
				tmpl := cfg.Templates[name]
				model := tmpl.Model
				if model == "" {
					model = "(default)"
				}
				agent := templateOwnOptions(tmpl).AgentFile
				if agent == "" {
					agent = "-"
				}
				ws := tmpl.Workspace
				if ws == "" {
					ws = "(default)"
				}
				fmt.Printf("  %-20s %-10s %-20s %s\n", name, model, agent, ws)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func newTemplateShowCmd() *cobra.Command {
	var resolved, asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a template's configuration",
		Long: `Print the configuration for a single template. By default the literal
YAML-defined fields are shown. Pass --resolved to print the effective
configuration an ephemeral agent would receive, with the top-level
'defaults' block cascaded in for unset fields. --json emits a structured
JSON document instead of a human table; combine --resolved --json for
machine-readable effective config.`,
		Example: `  leo template show coding
  leo template show coding --resolved
  leo template show coding --json
  leo template show coding --resolved --json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTemplateNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			tmpl, ok := cfg.Templates[name]
			if !ok {
				return fmt.Errorf("template %q not found", name)
			}

			if resolved {
				eff, err := resolveTemplate(cfg, tmpl)
				if err != nil {
					return fmt.Errorf("template %q: %w", name, err)
				}
				if asJSON {
					enc := json.NewEncoder(templateStdout)
					enc.SetIndent("", "  ")
					return enc.Encode(effectiveTemplateJSON(name, eff))
				}
				printResolvedTemplate(name, eff)
				return nil
			}

			if asJSON {
				enc := json.NewEncoder(templateStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(literalTemplateJSON(name, tmpl))
			}
			printTemplate(name, tmpl)
			return nil
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "show effective config with defaults cascade applied")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func newTemplateAddCmd() *cobra.Command {
	var (
		name           string
		workspace      string
		channels       string
		model          string
		agent          string
		permissionMode string
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new agent template",
		Long: `Add a new agent template to leo.yaml. Required: --name. Other fields may be
supplied via flags or, when a TTY is attached and any field is missing,
prompted for interactively. Templates are blueprints for ephemeral agents
spawned with 'leo agent spawn <template>'.`,
		Example: `  leo template add --name coding --model opus --agent dev
  leo template add --name ops --channels plugin:telegram@claude-plugins-official
  leo template add   # interactive`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			interactive := templateIsTTY()
			if interactive {
				reader := templateReader()
				if name == "" {
					name = promptLine(reader, "Template name: ")
				}
				if !cmd.Flags().Changed("workspace") {
					workspace = promptLine(reader, "Workspace (blank = default): ")
				}
				if !cmd.Flags().Changed("channels") {
					channels = promptLine(reader, "Channels (comma-separated plugin IDs, optional): ")
				}
				if !cmd.Flags().Changed("model") {
					model = promptLine(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
				}
				if !cmd.Flags().Changed("agent") {
					agent = promptLine(reader, "Agent (optional): ")
				}
				if !cmd.Flags().Changed("permission-mode") {
					permissionMode = promptLine(reader, "Permission mode (optional): ")
				}
			}

			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			if cfg.Templates == nil {
				cfg.Templates = make(map[string]config.TemplateConfig)
			}
			if _, exists := cfg.Templates[name]; exists {
				return fmt.Errorf("template %q already exists", name)
			}

			tmpl := config.TemplateConfig{
				Workspace: strings.TrimSpace(workspace),
				Channels:  splitAndTrim(channels),
				Model:     strings.TrimSpace(model),
			}
			// agent/permission-mode are claude harness options now — only set
			// the key when the value is non-empty; Validate() (via
			// DecodeOptions) catches bad permission_mode values.
			harnessOpts := map[string]any{}
			if a := strings.TrimSpace(agent); a != "" {
				harnessOpts["agent"] = a
			}
			if pm := strings.TrimSpace(permissionMode); pm != "" {
				harnessOpts["permission_mode"] = pm
			}
			if len(harnessOpts) > 0 {
				tmpl.HarnessOptions = harnessOpts
			}
			cfg.Templates[name] = tmpl

			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success.Printf("Template %q added.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Template name (required)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Template workspace directory (blank = default)")
	cmd.Flags().StringVar(&channels, "channels", "", "Comma-separated channel plugin IDs")
	cmd.Flags().StringVar(&model, "model", "", "Model override (defaults to global default)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent identifier (optional)")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "Permission mode (acceptEdits, auto, bypassPermissions, default, dontAsk, plan)")
	return cmd
}

func newTemplateRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a template from the config",
		Long: `Remove an agent template from leo.yaml. By default the command prompts
for confirmation when a TTY is attached. Pass --yes/-y to skip the prompt
(required for non-interactive use such as scripts or CI).`,
		Example: `  leo template remove coding
  leo template remove coding --yes`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTemplateNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Templates[name]; !ok {
				return fmt.Errorf("template %q not found", name)
			}
			if !yes {
				if !templateIsTTY() {
					return fmt.Errorf("refusing to remove template %q without --yes in non-interactive mode", name)
				}
				if !prompt.YesNo(templateReader(), fmt.Sprintf("Remove template %q?", name), false) {
					info.Println("Aborted.")
					return nil
				}
			}
			delete(cfg.Templates, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success.Printf("Template %q removed.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

// effectiveTemplate captures the effective template config after defaults
// cascade. Booleans are regular (non-pointer) because the cascade has already
// folded in defaults — callers read concrete values.
type effectiveTemplate struct {
	Workspace          string            `json:"workspace,omitempty"`
	Channels           []string          `json:"channels,omitempty"`
	DevChannels        []string          `json:"dev_channels,omitempty"`
	Model              string            `json:"model,omitempty"`
	MaxTurns           int               `json:"max_turns,omitempty"`
	BypassPermissions  bool              `json:"bypass_permissions"`
	RemoteControl      bool              `json:"remote_control"`
	MCPConfig          string            `json:"mcp_config,omitempty"`
	AddDirs            []string          `json:"add_dirs,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Agent              string            `json:"agent,omitempty"`
	AllowedTools       []string          `json:"allowed_tools,omitempty"`
	DisallowedTools    []string          `json:"disallowed_tools,omitempty"`
	AppendSystemPrompt string            `json:"append_system_prompt,omitempty"`
	PermissionMode     string            `json:"permission_mode,omitempty"`
}

// resolveTemplate returns an effective template config with defaults from
// cfg.Defaults cascaded in for any unset field. The template's own values
// always win; defaults only fill gaps.
//
// Claude options (permission_mode, allowed_tools, disallowed_tools,
// append_system_prompt, agent, bypass_permissions) are sourced by decoding
// the MERGED harness_options map (cfg.TemplateHarnessOptions), which
// reproduces the old per-field fallback-to-defaults cascade — see
// internal/config/harness.go's scopeHarnessOptions. remote_control keeps the
// pre-migration quirk exactly as internal/agent/args.go's BuildTemplateArgs
// does it: the defaults layer never applies to it, only the template's own
// options can turn it off (default true).
func resolveTemplate(cfg *config.Config, tmpl config.TemplateConfig) (effectiveTemplate, error) {
	decoded, err := claudeharness.Claude{}.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return effectiveTemplate{}, fmt.Errorf("decoding harness_options: %w", err)
	}
	opts, _ := decoded.(claudeharness.Options)

	eff := effectiveTemplate{
		Workspace:          tmpl.Workspace,
		Channels:           tmpl.Channels,
		DevChannels:        tmpl.DevChannels,
		Model:              tmpl.Model,
		MaxTurns:           tmpl.MaxTurns,
		BypassPermissions:  opts.BypassPermissions,
		MCPConfig:          tmpl.MCPConfig,
		AddDirs:            tmpl.AddDirs,
		Env:                tmpl.Env,
		Agent:              opts.AgentFile,
		AllowedTools:       opts.AllowedTools,
		DisallowedTools:    opts.DisallowedTools,
		AppendSystemPrompt: opts.AppendSystemPrompt,
		PermissionMode:     opts.PermissionMode,
	}
	if eff.Model == "" {
		eff.Model = cfg.Defaults.Model
	}
	if eff.Model == "" {
		eff.Model = config.DefaultModel
	}
	if eff.MaxTurns == 0 {
		eff.MaxTurns = cfg.Defaults.MaxTurns
	}
	if eff.MaxTurns == 0 {
		eff.MaxTurns = config.DefaultMaxTurns
	}
	eff.RemoteControl = true
	if v, ok := tmpl.HarnessOptions["remote_control"].(bool); ok {
		eff.RemoteControl = v
	}
	return eff, nil
}

// literalTemplateJSON reshapes a TemplateConfig into a JSON-friendly struct
// that mirrors `show` output without the effective-config cascade. Claude
// options are no longer broken into individual fields — HarnessOptions
// carries the template's own (unmerged) harness_options map verbatim. This
// breaks the prior JSON shape; acceptable for a solo-user project (see
// docs/configuration/harnesses.md).
type literalTemplatePayload struct {
	Name           string            `json:"name"`
	Workspace      string            `json:"workspace,omitempty"`
	Channels       []string          `json:"channels,omitempty"`
	DevChannels    []string          `json:"dev_channels,omitempty"`
	Model          string            `json:"model,omitempty"`
	MaxTurns       int               `json:"max_turns,omitempty"`
	MCPConfig      string            `json:"mcp_config,omitempty"`
	AddDirs        []string          `json:"add_dirs,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	HarnessOptions map[string]any    `json:"harness_options,omitempty"`
}

func literalTemplateJSON(name string, tmpl config.TemplateConfig) literalTemplatePayload {
	return literalTemplatePayload{
		Name:           name,
		Workspace:      tmpl.Workspace,
		Channels:       tmpl.Channels,
		DevChannels:    tmpl.DevChannels,
		Model:          tmpl.Model,
		MaxTurns:       tmpl.MaxTurns,
		MCPConfig:      tmpl.MCPConfig,
		AddDirs:        tmpl.AddDirs,
		Env:            redact.EnvMap(tmpl.Env),
		HarnessOptions: tmpl.HarnessOptions,
	}
}

// effectiveTemplateJSON wraps effectiveTemplate with a Name field for output.
type effectiveTemplatePayload struct {
	Name string `json:"name"`
	effectiveTemplate
}

func effectiveTemplateJSON(name string, eff effectiveTemplate) effectiveTemplatePayload {
	// Mask credentials on the way out; the caller's effectiveTemplate keeps
	// its real values (nothing else consumes this payload).
	eff.Env = redact.EnvMap(eff.Env)
	return effectiveTemplatePayload{Name: name, effectiveTemplate: eff}
}

func printTemplate(name string, tmpl config.TemplateConfig) {
	fmt.Printf("Template: %s\n", name)
	printField("Workspace", tmpl.Workspace)
	printField("Model", tmpl.Model)
	// Claude options are the template's OWN (unmerged) harness_options —
	// what this template itself declares, not the effective/cascaded view.
	opts := templateOwnOptions(tmpl)
	printField("Agent", opts.AgentFile)
	printField("Permission mode", opts.PermissionMode)
	if v, ok := tmpl.HarnessOptions["remote_control"].(bool); ok {
		printField("Remote control", fmt.Sprintf("%t", v))
	}
	if tmpl.MaxTurns > 0 {
		printField("Max turns", fmt.Sprintf("%d", tmpl.MaxTurns))
	}
	if len(tmpl.Channels) > 0 {
		printField("Channels", strings.Join(tmpl.Channels, ", "))
	}
	if len(opts.AllowedTools) > 0 {
		printField("Allowed tools", strings.Join(opts.AllowedTools, ", "))
	}
	if len(opts.DisallowedTools) > 0 {
		printField("Disallowed tools", strings.Join(opts.DisallowedTools, ", "))
	}
	if len(tmpl.AddDirs) > 0 {
		printField("Additional dirs", strings.Join(tmpl.AddDirs, ", "))
	}
	if opts.AppendSystemPrompt != "" {
		printField("Append system prompt", opts.AppendSystemPrompt)
	}
	if tmpl.MCPConfig != "" {
		printField("MCP config", tmpl.MCPConfig)
	}
	printEnvField(tmpl.Env)
}

// printEnvField renders an env map as sorted KEY=VALUE pairs with
// credential-looking values masked. `leo template show` is a command agents
// run, so an unmasked token here lands in a transcript; read leo.yaml
// directly when the real value is needed.
func printEnvField(env map[string]string) {
	if len(env) == 0 {
		return
	}
	keys := redact.Keys(env)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, redact.Value(k, env[k])))
	}
	printField("Env", strings.Join(pairs, " "))
}

// printResolvedTemplate renders the effective template config. Mirrors
// printTemplate but always emits model and max_turns since the cascade
// guarantees they carry a value.
func printResolvedTemplate(name string, eff effectiveTemplate) {
	fmt.Printf("Template: %s (resolved)\n", name)
	printField("Workspace", eff.Workspace)
	printField("Model", eff.Model)
	printField("Agent", eff.Agent)
	printField("Permission mode", eff.PermissionMode)
	printField("Remote control", fmt.Sprintf("%t", eff.RemoteControl))
	printField("Bypass permissions", fmt.Sprintf("%t", eff.BypassPermissions))
	if eff.MaxTurns > 0 {
		printField("Max turns", fmt.Sprintf("%d", eff.MaxTurns))
	}
	if len(eff.Channels) > 0 {
		printField("Channels", strings.Join(eff.Channels, ", "))
	}
	if len(eff.AllowedTools) > 0 {
		printField("Allowed tools", strings.Join(eff.AllowedTools, ", "))
	}
	if len(eff.DisallowedTools) > 0 {
		printField("Disallowed tools", strings.Join(eff.DisallowedTools, ", "))
	}
	if len(eff.AddDirs) > 0 {
		printField("Additional dirs", strings.Join(eff.AddDirs, ", "))
	}
	if eff.AppendSystemPrompt != "" {
		printField("Append system prompt", eff.AppendSystemPrompt)
	}
	if eff.MCPConfig != "" {
		printField("MCP config", eff.MCPConfig)
	}
	printEnvField(eff.Env)
}

func printField(label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-22s %s\n", label+":", value)
}

func completeTemplateNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		if strings.HasPrefix(name, toComplete) {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
