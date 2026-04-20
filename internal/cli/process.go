package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

// Testability seams — overridden in tests so confirm-on-remove and the
// interactive add flow can be exercised without a real TTY.
var (
	processStdin  io.Reader = os.Stdin
	processStdout io.Writer = os.Stdout
	processIsTTY            = defaultProcessIsTTY
)

// defaultProcessIsTTY reports whether stdin is an interactive character device.
func defaultProcessIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func newProcessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "process",
		Short: "Manage supervised processes",
		Long: `Manage the long-running Claude processes supervised by leo.

Each process is a named entry in leo.yaml with its own workspace, channel
plugin list, and model. Subcommands here add/remove/enable/disable entries
and inspect running state. Use 'leo service start' to launch the supervisor
after editing the set.`,
		Example: `  leo process list
  leo process add my-bot --model sonnet --channels plugin:telegram@claude-plugins-official
  leo process disable my-bot
  leo process remove my-bot`,
	}

	cmd.AddCommand(
		newProcessListCmd(),
		newProcessAddCmd(),
		newProcessRemoveCmd(),
		newProcessEnableCmd(),
		newProcessDisableCmd(),
		newProcessAttachCmd(),
		newProcessLogsCmd(),
	)
	return cmd
}

// --- attach ---

func newProcessAttachCmd() *cobra.Command {
	var host string
	var cc bool
	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to a supervised process's tmux session",
		Long: `Attach to the tmux session of a configured process. Locally this
replaces the current process with tmux so the TUI owns the TTY cleanly.
Remotely it runs 'ssh -t <host> tmux attach -t leo-<name>'. Detach with the
normal tmux prefix + d (default: C-b d).`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, res, err := dispatch(host)
			if err != nil {
				return err
			}
			// Processes use a stable session name — no daemon round-trip needed
			// for either local or remote dispatch.
			return attachTmuxSession(res, processSessionName(name), attachOptions{cc: cc})
		},
	}
	addHostFlag(cmd, &host)
	addControlModeFlag(cmd, &cc)
	return cmd
}

// --- logs ---

func newProcessLogsCmd() *cobra.Command {
	var host string
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show recent output from a supervised process's tmux pane",
		Long: `Capture the tmux pane of a configured process. --follow streams new
output via a tail -f on a tmux pipe-pane log (use Ctrl-C to exit).`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, res, err := dispatch(host)
			if err != nil {
				return err
			}

			session := processSessionName(name)
			if follow {
				return followTmuxSession(res, session, lines)
			}
			return captureTmuxPane(res, session, lines)
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "number of trailing lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream output (tail -f)")
	return cmd
}

// processListEntry is the JSON-friendly view emitted by --json. Fields mirror
// the columns shown in the human-readable list so scripts can consume the
// same data without scraping the table.
type processListEntry struct {
	Name      string                  `json:"name"`
	Model     string                  `json:"model"`
	Enabled   bool                    `json:"enabled"`
	Status    string                  `json:"status"`
	Workspace string                  `json:"workspace"`
	Channels  []string                `json:"channels"`
	Runtime   *processRuntimeStateDTO `json:"runtime,omitempty"`
}

// processRuntimeStateDTO mirrors the daemon's ProcessStateInfo for JSON output.
// Kept separate from the daemon type so the CLI contract does not leak
// server-side field renames.
type processRuntimeStateDTO struct {
	Status    string `json:"status"`
	Restarts  int    `json:"restarts"`
	Ephemeral bool   `json:"ephemeral,omitempty"`
}

func newProcessListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured processes and their runtime status",
		Long: `List every process defined in leo.yaml along with its configured
model, workspace, channels, and (when the daemon is running) live supervisor
state. Use --json for machine-readable output.`,
		Example: `  leo process list
  leo process list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Try to get runtime state from daemon so both JSON and table
			// output see the same snapshot.
			var states map[string]daemon.ProcessStateInfo
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cmd.Context(), cfg.HomePath, "GET", "/process/list", nil)
				if err == nil && resp.OK {
					if jsonErr := json.Unmarshal(resp.Data, &states); jsonErr != nil {
						warn.Printf("  Could not parse process state: %v\n", jsonErr)
					}
				}
			}

			if asJSON {
				return writeProcessListJSON(cfg, states)
			}

			if len(cfg.Processes) == 0 {
				_, _ = info.Fprintln(processStdout, "No processes configured.")
				return nil
			}

			// Deterministic order — iteration over a map is otherwise random.
			names := make([]string, 0, len(cfg.Processes))
			for name := range cfg.Processes {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				proc := cfg.Processes[name]
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

				fmt.Fprintf(processStdout, "  %-20s %-8s %-8s%s\n", name, model, status, runtime)
				fmt.Fprintf(processStdout, "    workspace: %s\n", ws)
				if channels != "-" {
					fmt.Fprintf(processStdout, "    channels:  %s\n", channels)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// writeProcessListJSON writes a deterministic, sorted JSON list of processes
// with merged runtime state to processStdout.
func writeProcessListJSON(cfg *config.Config, states map[string]daemon.ProcessStateInfo) error {
	names := make([]string, 0, len(cfg.Processes))
	for name := range cfg.Processes {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]processListEntry, 0, len(names))
	for _, name := range names {
		proc := cfg.Processes[name]
		status := "disabled"
		if proc.Enabled {
			status = "enabled"
		}
		entry := processListEntry{
			Name:      name,
			Model:     cfg.ProcessModel(proc),
			Enabled:   proc.Enabled,
			Status:    status,
			Workspace: cfg.ProcessWorkspace(proc),
			Channels:  append([]string{}, proc.Channels...),
		}
		if state, ok := states[name]; ok {
			entry.Runtime = &processRuntimeStateDTO{
				Status:    state.Status,
				Restarts:  state.Restarts,
				Ephemeral: state.Ephemeral,
			}
		}
		entries = append(entries, entry)
	}

	enc := json.NewEncoder(processStdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
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
		Long: `Add a new supervised process to leo.yaml.

Any field passed via flag is used verbatim. When stdin is a TTY the remaining
fields are prompted interactively. In non-interactive runs missing fields
fall through to the configured defaults (model) or empty values (workspace,
channels, agent).`,
		Example: `  # Fully interactive
  leo process add my-bot

  # Fully scripted
  leo process add my-bot --model sonnet \
      --channels plugin:telegram@claude-plugins-official \
      --workspace ~/projects/my-bot`,
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

			// Prompt interactively for every field not supplied via flag,
			// but only when stdin is a TTY. Non-TTY runs (CI, scripts)
			// accept whatever flags were passed and leave the rest empty
			// so the flow is still deterministic.
			if processIsTTY() {
				reader := bufio.NewReader(processStdin)
				if !cmd.Flags().Changed("workspace") {
					workspace = promptLine(reader, "Workspace (blank = default): ")
				}
				if !cmd.Flags().Changed("channels") {
					channels = promptLine(reader, "Channels (comma-separated, optional): ")
				}
				if !cmd.Flags().Changed("model") {
					model = promptLine(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
				}
				if !cmd.Flags().Changed("agent") {
					agent = promptLine(reader, "Agent (optional): ")
				}
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

			success.Fprintf(processStdout, "Process %q added.\n", name)
			if daemon.IsRunning(cfg.HomePath) {
				_, _ = warn.Fprintln(processStdout, "Run `leo service restart` to apply process changes.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Process workspace directory (blank = default)")
	cmd.Flags().StringVar(&channels, "channels", "", "Comma-separated channel plugin IDs (e.g. plugin:telegram@claude-plugins-official)")
	cmd.Flags().StringVar(&model, "model", "", "Model override (defaults to global default)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent identifier (optional)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Create the process in a disabled state")
	return cmd
}

func newProcessRemoveCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a process from the config",
		Long: `Remove a process entry from leo.yaml. Prompts for confirmation by
default when stdin is a TTY. In non-interactive runs (pipes, CI) pass --yes
to confirm up front; otherwise the command refuses rather than silently
deleting config.`,
		Example: `  leo process remove my-bot
  leo process remove my-bot --yes`,
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

			if !assumeYes {
				if !processIsTTY() {
					return fmt.Errorf("refusing to remove %q without confirmation: pass --yes to skip the prompt", name)
				}
				reader := bufio.NewReader(processStdin)
				if !prompt.YesNo(reader, fmt.Sprintf("Remove process %q?", name), false) {
					_, _ = info.Fprintln(processStdout, "Cancelled.")
					return nil
				}
			}

			delete(cfg.Processes, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success.Fprintf(processStdout, "Process %q removed.\n", name)
			if daemon.IsRunning(cfg.HomePath) {
				_, _ = warn.Fprintln(processStdout, "Run `leo service restart` to apply process changes.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
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
