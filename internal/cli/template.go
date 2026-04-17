package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

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
						Agent:     tmpl.Agent,
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
				agent := tmpl.Agent
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
				eff := resolveTemplate(cfg, tmpl)
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
				Workspace:      strings.TrimSpace(workspace),
				Channels:       splitAndTrim(channels),
				Model:          strings.TrimSpace(model),
				Agent:          strings.TrimSpace(agent),
				PermissionMode: strings.TrimSpace(permissionMode),
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
// Note: TemplateConfig does not have a BypassPermissions field today, so that
// flag is sourced from defaults alone. AllowedTools / DisallowedTools fall
// through to defaults only when the template leaves them empty — we do not
// merge, matching the intent of "template shadows defaults entirely for that
// field" used elsewhere in the codebase.
func resolveTemplate(cfg *config.Config, tmpl config.TemplateConfig) effectiveTemplate {
	eff := effectiveTemplate{
		Workspace:          tmpl.Workspace,
		Channels:           tmpl.Channels,
		DevChannels:        tmpl.DevChannels,
		Model:              tmpl.Model,
		MaxTurns:           tmpl.MaxTurns,
		BypassPermissions:  cfg.Defaults.BypassPermissions,
		MCPConfig:          tmpl.MCPConfig,
		AddDirs:            tmpl.AddDirs,
		Env:                tmpl.Env,
		Agent:              tmpl.Agent,
		AllowedTools:       tmpl.AllowedTools,
		DisallowedTools:    tmpl.DisallowedTools,
		AppendSystemPrompt: tmpl.AppendSystemPrompt,
		PermissionMode:     tmpl.PermissionMode,
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
	// Match internal/agent/args.go: templates default remote-control to true
	// when unset on the template, independent of cfg.Defaults.RemoteControl.
	eff.RemoteControl = true
	if tmpl.RemoteControl != nil {
		eff.RemoteControl = *tmpl.RemoteControl
	}
	if eff.PermissionMode == "" {
		eff.PermissionMode = cfg.Defaults.PermissionMode
	}
	if len(eff.AllowedTools) == 0 {
		eff.AllowedTools = cfg.Defaults.AllowedTools
	}
	if len(eff.DisallowedTools) == 0 {
		eff.DisallowedTools = cfg.Defaults.DisallowedTools
	}
	if eff.AppendSystemPrompt == "" {
		eff.AppendSystemPrompt = cfg.Defaults.AppendSystemPrompt
	}
	return eff
}

// literalTemplateJSON reshapes a TemplateConfig into a JSON-friendly struct
// that mirrors `show` output without the effective-config cascade.
type literalTemplatePayload struct {
	Name               string            `json:"name"`
	Workspace          string            `json:"workspace,omitempty"`
	Channels           []string          `json:"channels,omitempty"`
	DevChannels        []string          `json:"dev_channels,omitempty"`
	Model              string            `json:"model,omitempty"`
	MaxTurns           int               `json:"max_turns,omitempty"`
	RemoteControl      *bool             `json:"remote_control,omitempty"`
	MCPConfig          string            `json:"mcp_config,omitempty"`
	AddDirs            []string          `json:"add_dirs,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Agent              string            `json:"agent,omitempty"`
	AllowedTools       []string          `json:"allowed_tools,omitempty"`
	DisallowedTools    []string          `json:"disallowed_tools,omitempty"`
	AppendSystemPrompt string            `json:"append_system_prompt,omitempty"`
	PermissionMode     string            `json:"permission_mode,omitempty"`
}

func literalTemplateJSON(name string, tmpl config.TemplateConfig) literalTemplatePayload {
	return literalTemplatePayload{
		Name:               name,
		Workspace:          tmpl.Workspace,
		Channels:           tmpl.Channels,
		DevChannels:        tmpl.DevChannels,
		Model:              tmpl.Model,
		MaxTurns:           tmpl.MaxTurns,
		RemoteControl:      tmpl.RemoteControl,
		MCPConfig:          tmpl.MCPConfig,
		AddDirs:            tmpl.AddDirs,
		Env:                tmpl.Env,
		Agent:              tmpl.Agent,
		AllowedTools:       tmpl.AllowedTools,
		DisallowedTools:    tmpl.DisallowedTools,
		AppendSystemPrompt: tmpl.AppendSystemPrompt,
		PermissionMode:     tmpl.PermissionMode,
	}
}

// effectiveTemplateJSON wraps effectiveTemplate with a Name field for output.
type effectiveTemplatePayload struct {
	Name string `json:"name"`
	effectiveTemplate
}

func effectiveTemplateJSON(name string, eff effectiveTemplate) effectiveTemplatePayload {
	return effectiveTemplatePayload{Name: name, effectiveTemplate: eff}
}

func printTemplate(name string, tmpl config.TemplateConfig) {
	fmt.Printf("Template: %s\n", name)
	printField("Workspace", tmpl.Workspace)
	printField("Model", tmpl.Model)
	printField("Agent", tmpl.Agent)
	printField("Permission mode", tmpl.PermissionMode)
	if tmpl.RemoteControl != nil {
		printField("Remote control", fmt.Sprintf("%t", *tmpl.RemoteControl))
	}
	if tmpl.MaxTurns > 0 {
		printField("Max turns", fmt.Sprintf("%d", tmpl.MaxTurns))
	}
	if len(tmpl.Channels) > 0 {
		printField("Channels", strings.Join(tmpl.Channels, ", "))
	}
	if len(tmpl.AllowedTools) > 0 {
		printField("Allowed tools", strings.Join(tmpl.AllowedTools, ", "))
	}
	if len(tmpl.DisallowedTools) > 0 {
		printField("Disallowed tools", strings.Join(tmpl.DisallowedTools, ", "))
	}
	if len(tmpl.AddDirs) > 0 {
		printField("Additional dirs", strings.Join(tmpl.AddDirs, ", "))
	}
	if tmpl.AppendSystemPrompt != "" {
		printField("Append system prompt", tmpl.AppendSystemPrompt)
	}
	if tmpl.MCPConfig != "" {
		printField("MCP config", tmpl.MCPConfig)
	}
	if len(tmpl.Env) > 0 {
		keys := make([]string, 0, len(tmpl.Env))
		for k := range tmpl.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, tmpl.Env[k]))
		}
		printField("Env", strings.Join(pairs, " "))
	}
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
	if len(eff.Env) > 0 {
		keys := make([]string, 0, len(eff.Env))
		for k := range eff.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, eff.Env[k]))
		}
		printField("Env", strings.Join(pairs, " "))
	}
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
