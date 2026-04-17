package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

// Testability seams for task subcommands. Tests override these to simulate
// interactive / non-interactive stdin and to stub out the YesNo prompt.
var (
	taskIsTTY                                                                = defaultIsTTY
	taskYesNo func(reader *bufio.Reader, label string, defaultYes bool) bool = prompt.YesNo
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage scheduled tasks",
		Long: `Manage scheduled tasks — persistent cron-style invocations of the claude CLI.

A task couples a cron schedule to a prompt file in a workspace. When the cron
fires, leo spawns claude with the prompt (plus assembled context) and lets the
agent drive any configured channel plugins (Telegram, Slack, webhook, etc.)
to deliver results. Tasks live in leo.yaml under the ` + "`tasks:`" + ` map.

Use the subcommands to add / list / enable / disable / remove tasks, view
execution history, or tail the most recent run's log.`,
		Example: `  # List configured tasks (optionally as JSON)
  leo task list
  leo task list --json

  # Add a task non-interactively
  leo task add \
    --name nightly-digest \
    --schedule "0 7 * * *" \
    --prompt-file prompts/nightly.md \
    --channels plugin:telegram@claude-plugins-official \
    --notify-on-fail

  # Add a task interactively (prompts for any missing required fields)
  leo task add

  # Remove a task (prompts for confirmation)
  leo task remove nightly-digest
  leo task remove nightly-digest --yes

  # Inspect history and tail the last run's log
  leo task history nightly-digest
  leo task logs nightly-digest -n 50`,
	}

	cmd.AddCommand(
		newTaskListCmd(),
		newTaskAddCmd(),
		newTaskRemoveCmd(),
		newTaskEnableCmd(),
		newTaskDisableCmd(),
		newTaskHistoryCmd(),
		newTaskLogsCmd(),
	)

	return cmd
}

// taskListEntry is the JSON shape emitted by `leo task list --json`. Kept
// stable and minimal so downstream scripts can rely on the field names.
type taskListEntry struct {
	Name     string     `json:"name"`
	Schedule string     `json:"schedule"`
	Model    string     `json:"model"`
	Enabled  bool       `json:"enabled"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`
}

func newTaskListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Get next run times from daemon if available
			var nextRuns map[string]time.Time
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/cron/list", nil)
				if err == nil && resp.OK {
					var entries []cron.EntryInfo
					if json.Unmarshal(resp.Data, &entries) == nil {
						nextRuns = make(map[string]time.Time, len(entries))
						for _, e := range entries {
							nextRuns[e.Name] = e.Next
						}
					}
				}
			}

			hist := history.NewStore(cfg.HomePath)

			if asJSON {
				out := make([]taskListEntry, 0, len(cfg.Tasks))
				for name, task := range cfg.Tasks {
					entry := taskListEntry{
						Name:     name,
						Schedule: task.Schedule,
						Model:    cfg.TaskModel(task),
						Enabled:  task.Enabled,
					}
					if e := hist.Get(name); e != nil {
						t := e.RunAt
						entry.LastRun = &t
					}
					if t, ok := nextRuns[name]; ok {
						tt := t
						entry.NextRun = &tt
					}
					out = append(out, entry)
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(cfg.Tasks) == 0 {
				info.Println("No tasks configured.")
				return nil
			}

			fmt.Printf("  %-20s %-18s %-8s %-8s %-20s %s\n", "NAME", "SCHEDULE", "MODEL", "STATUS", "LAST RUN", "NEXT RUN")
			for name, task := range cfg.Tasks {
				status := "disabled"
				if task.Enabled {
					status = "enabled"
				}

				model := cfg.TaskModel(task)
				lastRun := ""
				if e := hist.Get(name); e != nil {
					result := "ok"
					if e.ExitCode != 0 {
						result = fmt.Sprintf("FAIL (exit %d)", e.ExitCode)
					}
					lastRun = fmt.Sprintf("%s %s", e.RunAt.Format("Jan 02 15:04"), result)
				}
				nextRun := ""
				if t, ok := nextRuns[name]; ok {
					nextRun = t.Local().Format("Jan 02 15:04")
				}
				fmt.Printf("  %-20s %-18s %-8s %-8s %-20s %s\n", name, task.Schedule, model, status, lastRun, nextRun)
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func newTaskAddCmd() *cobra.Command {
	var (
		flagName         string
		flagSchedule     string
		flagPromptFile   string
		flagModel        string
		flagChannels     string
		flagNotifyOnFail bool
		flagSilent       bool
		flagDisabled     bool
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new scheduled task",
		Long: `Add a scheduled task to leo.yaml.

With all required flags (--name, --schedule, --prompt-file) this runs
non-interactively. If any required field is missing and stdin is a TTY the
command falls back to an interactive wizard that prompts only for the
missing values. When stdin is not a TTY and required fields are missing the
command exits with a non-zero status and lists which flags are missing.`,
		Example: `  # Fully non-interactive
  leo task add \
    --name nightly-digest \
    --schedule "0 7 * * *" \
    --prompt-file prompts/nightly.md \
    --model sonnet \
    --channels plugin:telegram@claude-plugins-official,plugin:slack@claude-plugins-official \
    --notify-on-fail

  # Interactive wizard (prompts for any missing fields)
  leo task add`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			name := strings.TrimSpace(flagName)
			schedule := strings.TrimSpace(flagSchedule)
			promptFile := strings.TrimSpace(flagPromptFile)
			model := strings.TrimSpace(flagModel)
			channelsStr := flagChannels
			notifyOnFail := flagNotifyOnFail
			silent := flagSilent
			disabled := flagDisabled

			hasAllRequired := name != "" && schedule != "" && promptFile != ""

			if !hasAllRequired {
				if !taskIsTTY() {
					missing := missingAddFlags(name, schedule, promptFile)
					return fmt.Errorf("missing required flag(s) for non-interactive add: %s", strings.Join(missing, ", "))
				}

				reader := bufio.NewReader(os.Stdin)
				if name == "" {
					name = promptLine(reader, "Task name: ")
				}
				if schedule == "" {
					schedule = promptLine(reader, "Cron schedule (e.g. '0 7 * * *'): ")
				}
				if promptFile == "" {
					promptFile = promptLine(reader, "Prompt file (relative to workspace): ")
				}
				if model == "" && !cmd.Flags().Changed("model") {
					model = promptLine(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
				}
				if channelsStr == "" && !cmd.Flags().Changed("channels") {
					channelsStr = promptLine(reader, "Channels (comma-separated plugin IDs, optional): ")
				}
				if !cmd.Flags().Changed("notify-on-fail") {
					notifyStr := promptLine(reader, "Notify configured channels on failure? [y/N]: ")
					notifyOnFail = strings.EqualFold(strings.TrimSpace(notifyStr), "y")
				}
				if !cmd.Flags().Changed("silent") {
					silentStr := promptLine(reader, "Silent mode? [y/N]: ")
					silent = strings.EqualFold(strings.TrimSpace(silentStr), "y")
				}
			}

			if name == "" || schedule == "" || promptFile == "" {
				missing := missingAddFlags(name, schedule, promptFile)
				return fmt.Errorf("missing required field(s): %s", strings.Join(missing, ", "))
			}

			var channels []string
			if channelsStr != "" {
				for _, ch := range strings.Split(channelsStr, ",") {
					if c := strings.TrimSpace(ch); c != "" {
						channels = append(channels, c)
					}
				}
			}

			task := config.TaskConfig{
				Schedule:     schedule,
				PromptFile:   promptFile,
				Model:        model,
				Channels:     channels,
				NotifyOnFail: notifyOnFail,
				Enabled:      !disabled,
				Silent:       silent,
			}

			if cfg.Tasks == nil {
				cfg.Tasks = make(map[string]config.TaskConfig)
			}
			if _, exists := cfg.Tasks[name]; exists {
				return fmt.Errorf("task %q already exists — remove it first or pick a different --name", name)
			}
			cfg.Tasks[name] = task

			cfgPath, err := configPath()
			if err != nil {
				return err
			}

			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}

			success.Printf("Task %q added.\n", name)

			// Warn if the prompt file doesn't exist yet (common when creating
			// a task before authoring its prompt).
			ws := cfg.TaskWorkspace(task)
			if abs, resolveErr := config.ResolvePromptPath(ws, task.PromptFile); resolveErr == nil {
				if _, statErr := os.Stat(abs); statErr != nil {
					warn.Printf("Prompt file %s does not exist yet — create it before the task runs.\n", abs)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flagName, "name", "", "task name (required for non-interactive add)")
	cmd.Flags().StringVar(&flagSchedule, "schedule", "", "cron schedule, e.g. '0 7 * * *' (required for non-interactive add)")
	cmd.Flags().StringVar(&flagPromptFile, "prompt-file", "", "prompt file path relative to the task workspace (required)")
	cmd.Flags().StringVar(&flagModel, "model", "", "claude model override (defaults.model is used when empty)")
	cmd.Flags().StringVar(&flagChannels, "channels", "", "comma-separated channel plugin IDs")
	cmd.Flags().BoolVar(&flagNotifyOnFail, "notify-on-fail", false, "spawn a child claude to notify configured channels on failure")
	cmd.Flags().BoolVar(&flagSilent, "silent", false, "silent mode — skip chatter on successful runs")
	cmd.Flags().BoolVar(&flagDisabled, "disabled", false, "create the task in a disabled state")

	return cmd
}

// missingAddFlags reports which of the required flags are still empty.
func missingAddFlags(name, schedule, promptFile string) []string {
	var missing []string
	if name == "" {
		missing = append(missing, "--name")
	}
	if schedule == "" {
		missing = append(missing, "--schedule")
	}
	if promptFile == "" {
		missing = append(missing, "--prompt-file")
	}
	return missing
}

func newTaskRemoveCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove a task from the config",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			name := args[0]

			if _, ok := cfg.Tasks[name]; !ok && !daemon.IsRunning(cfg.HomePath) {
				return fmt.Errorf("task %q not found", name)
			}

			if !assumeYes {
				if !taskIsTTY() {
					return fmt.Errorf("refusing to remove task %q without confirmation: pass --yes to skip the prompt when stdin is not a TTY", name)
				}
				reader := bufio.NewReader(os.Stdin)
				label := fmt.Sprintf("Remove task %q? This cannot be undone.", name)
				if !taskYesNo(reader, label, false) {
					info.Println("Aborted.")
					return nil
				}
			}

			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "POST", "/task/remove",
					daemon.TaskNameRequest{Name: name})
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Printf("Task %q removed (via daemon).\n", name)
				return nil
			}

			if _, ok := cfg.Tasks[name]; !ok {
				return fmt.Errorf("task %q not found", name)
			}

			delete(cfg.Tasks, name)

			cfgPath, err := configPath()
			if err != nil {
				return err
			}

			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}

			success.Printf("Task %q removed.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func newTaskEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "enable <name>",
		Short:             "Enable a task",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setTaskEnabled(args[0], true)
		},
	}
}

func newTaskDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "disable <name>",
		Short:             "Disable a task",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setTaskEnabled(args[0], false)
		},
	}
}

func setTaskEnabled(name string, enabled bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if daemon.IsRunning(cfg.HomePath) {
		path := "/task/enable"
		if !enabled {
			path = "/task/disable"
		}
		resp, err := daemon.Send(cfg.HomePath, "POST", path,
			daemon.TaskNameRequest{Name: name})
		if err != nil {
			return fmt.Errorf("daemon request failed: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("daemon error: %s", resp.Error)
		}
		action := "enabled"
		if !enabled {
			action = "disabled"
		}
		success.Printf("Task %q %s (via daemon).\n", name, action)
		return nil
	}

	task, ok := cfg.Tasks[name]
	if !ok {
		return fmt.Errorf("task %q not found", name)
	}

	task.Enabled = enabled
	cfg.Tasks[name] = task

	cfgPath, err := configPath()
	if err != nil {
		return err
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}

	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	success.Printf("Task %q %s.\n", name, action)
	return nil
}

func newTaskHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "history [task-name]",
		Short:             "Show task execution history",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			hist := history.NewStore(cfg.HomePath)

			if len(args) > 0 {
				taskName := args[0]
				entries := hist.GetAll(taskName)
				if len(entries) == 0 {
					info.Printf("No history for task %q\n", taskName)
					return nil
				}
				fmt.Printf("History for %q (last %d runs):\n\n", taskName, len(entries))
				for _, e := range entries {
					fmt.Printf("  %s  %s\n", e.RunAt.Local().Format("2006-01-02 15:04:05"), formatHistoryResult(e))
				}
				return nil
			}

			all := hist.All()
			if len(all) == 0 {
				info.Println("No task history.")
				return nil
			}

			fmt.Printf("  %-20s %-5s %s\n", "TASK", "RUNS", "LAST RUN")
			for task, entries := range all {
				if len(entries) == 0 {
					continue
				}
				last := entries[0]
				fmt.Printf("  %-20s %-5d %s %s\n", task, len(entries),
					last.RunAt.Local().Format("Jan 02 15:04"), formatHistoryResult(last))
			}
			return nil
		},
	}
}

func newTaskLogsCmd() *cobra.Command {
	var tailLines int
	cmd := &cobra.Command{
		Use:               "logs <task-name>",
		Short:             "Show the most recent log output for a task",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			taskName := args[0]
			hist := history.NewStore(cfg.HomePath)
			entry := hist.Get(taskName)
			if entry == nil {
				return fmt.Errorf("no history for task %q", taskName)
			}
			logPath := hist.LogPath(*entry)
			if logPath == "" {
				return fmt.Errorf("no log file recorded for latest run of %q", taskName)
			}

			f, err := os.Open(logPath)
			if err != nil {
				return fmt.Errorf("opening log: %w", err)
			}
			defer f.Close()

			if tailLines <= 0 {
				if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
					return fmt.Errorf("reading log: %w", err)
				}
				return nil
			}

			lines, err := readLastLines(f, tailLines)
			if err != nil {
				return fmt.Errorf("reading log tail: %w", err)
			}
			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&tailLines, "tail", "n", 0, "Show only the last N lines (0 = show all)")
	return cmd
}

// formatHistoryResult renders a history entry's outcome with its reason.
func formatHistoryResult(e history.Entry) string {
	if e.ExitCode == 0 {
		return "ok"
	}
	reason := e.Reason
	if reason == "" {
		reason = "failure"
	}
	return fmt.Sprintf("FAIL (%s, exit %d)", reason, e.ExitCode)
}

// readLastLines reads the last n lines from r. For small log files it is
// acceptable to load the whole file into memory.
func readLastLines(r io.Reader, n int) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

func configPath() (string, error) {
	if cfgFile != "" {
		return cfgFile, nil
	}
	return config.FindConfig("")
}

func promptLine(reader *bufio.Reader, label string) string {
	fmt.Print(label)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}
