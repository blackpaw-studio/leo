package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

func newProcessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "process",
		Short: "Manage supervised processes",
	}

	cmd.AddCommand(
		newProcessListCmd(),
		newProcessAddCmd(),
		newProcessRemoveCmd(),
		newProcessEnableCmd(),
		newProcessDisableCmd(),
	)
	return cmd
}

func newProcessListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured processes and their runtime status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Show config-defined processes
			if len(cfg.Processes) == 0 {
				info.Println("No processes configured.")
				return nil
			}

			// Try to get runtime state from daemon
			var states map[string]daemon.ProcessStateInfo
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/process/list", nil)
				if err == nil && resp.OK {
					if jsonErr := json.Unmarshal(resp.Data, &states); jsonErr != nil {
						warn.Printf("  Could not parse process state: %v\n", jsonErr)
					}
				}
			}

			for name, proc := range cfg.Processes {
				status := "disabled"
				if proc.Enabled {
					status = "enabled"
				}

				ws := cfg.ProcessWorkspace(proc)
				model := cfg.ProcessModel(proc)
				channels := "-"
				if len(proc.Channels) > 0 {
					channels = strings.Join(proc.Channels, ", ")
				}

				// Override with runtime status if available
				runtime := ""
				if state, ok := states[name]; ok {
					runtime = fmt.Sprintf("  [%s", state.Status)
					if state.Restarts > 0 {
						runtime += fmt.Sprintf(", %d restart(s)", state.Restarts)
					}
					runtime += "]"
				}

				fmt.Printf("  %-20s %-8s %-8s%s\n", name, model, status, runtime)
				fmt.Printf("    workspace: %s\n", ws)
				if channels != "-" {
					fmt.Printf("    channels:  %s\n", channels)
				}
			}
			return nil
		},
	}
}

func newProcessAddCmd() *cobra.Command {
	var (
		workspace string
		channels  string
		model     string
		agent     string
		disabled  bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a new supervised process",
		Long: `Add a new supervised process to the config.

When called with flags, the process is created non-interactively.
When called without flags, an interactive prompt is shown.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, exists := cfg.Processes[name]; exists {
				return fmt.Errorf("process %q already exists", name)
			}

			// Interactive mode when no flags were given.
			if !cmd.Flags().Changed("workspace") &&
				!cmd.Flags().Changed("channels") &&
				!cmd.Flags().Changed("model") &&
				!cmd.Flags().Changed("agent") {
				reader := bufio.NewReader(os.Stdin)
				workspace = promptLine(reader, "Workspace (blank = default): ")
				channels = promptLine(reader, "Channels (comma-separated, optional): ")
				model = promptLine(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
				agent = promptLine(reader, "Agent (optional): ")
			}

			proc := config.ProcessConfig{
				Workspace: workspace,
				Channels:  splitAndTrim(channels),
				Model:     model,
				Agent:     agent,
				Enabled:   !disabled,
			}

			if cfg.Processes == nil {
				cfg.Processes = make(map[string]config.ProcessConfig)
			}
			cfg.Processes[name] = proc

			if err := saveConfig(cfg); err != nil {
				return err
			}

			success.Printf("Process %q added.\n", name)
			if daemon.IsRunning(cfg.HomePath) {
				warn.Println("Run `leo service restart` to apply process changes.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Process workspace directory (blank = default)")
	cmd.Flags().StringVar(&channels, "channels", "", "Comma-separated telegram channels")
	cmd.Flags().StringVar(&model, "model", "", "Model override (defaults to global default)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent identifier (optional)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Create the process in a disabled state")
	return cmd
}

func newProcessRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove a process from the config",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Processes[name]; !ok {
				return fmt.Errorf("process %q not found", name)
			}
			delete(cfg.Processes, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success.Printf("Process %q removed.\n", name)
			if daemon.IsRunning(cfg.HomePath) {
				warn.Println("Run `leo service restart` to apply process changes.")
			}
			return nil
		},
	}
}

func newProcessEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "enable <name>",
		Short:             "Enable a process",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProcessEnabled(args[0], true)
		},
	}
}

func newProcessDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "disable <name>",
		Short:             "Disable a process",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProcessEnabled(args[0], false)
		},
	}
}

func setProcessEnabled(name string, enabled bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	proc, ok := cfg.Processes[name]
	if !ok {
		return fmt.Errorf("process %q not found", name)
	}
	if proc.Enabled == enabled {
		action := "enabled"
		if !enabled {
			action = "disabled"
		}
		info.Printf("Process %q already %s.\n", name, action)
		return nil
	}
	proc.Enabled = enabled
	cfg.Processes[name] = proc

	if err := saveConfig(cfg); err != nil {
		return err
	}
	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	success.Printf("Process %q %s.\n", name, action)
	if daemon.IsRunning(cfg.HomePath) {
		warn.Println("Run `leo service restart` to apply process changes.")
	}
	return nil
}

// saveConfig writes the config to its source path.
func saveConfig(cfg *config.Config) error {
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	return config.Save(cfgPath, cfg)
}

// splitAndTrim splits a comma-separated string and trims whitespace from each
// element, skipping empties. Returns nil for empty input.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
