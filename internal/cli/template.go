package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage ephemeral agent templates",
	}
	cmd.AddCommand(
		newTemplateListCmd(),
		newTemplateShowCmd(),
		newTemplateRemoveCmd(),
	)
	return cmd
}

func newTemplateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Templates) == 0 {
				info.Println("No templates configured.")
				return nil
			}

			names := make([]string, 0, len(cfg.Templates))
			for name := range cfg.Templates {
				names = append(names, name)
			}
			sort.Strings(names)

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
}

func newTemplateShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "show <name>",
		Short:             "Show a template's configuration",
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
			printTemplate(name, tmpl)
			return nil
		},
	}
}

func newTemplateRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove a template from the config",
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
			delete(cfg.Templates, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success.Printf("Template %q removed.\n", name)
			return nil
		},
	}
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
