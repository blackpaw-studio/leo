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
	"github.com/spf13/cobra"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage scheduled tasks",
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

func newTaskListCmd() *cobra.Command {
	return &cobra.Command{
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

			if len(cfg.Tasks) == 0 {
				info.Println("No tasks configured.")
				return nil
			}

			fmt.Printf("  %-20s %-18s %-8s %-8s %-20s %s\n", "NAME", "SCHEDULE", "MODEL", "STATUS", "LAST RUN", "NEXT RUN")
			hist := history.NewStore(cfg.HomePath)
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
}

func newTaskAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add",
		Short: "Add a new scheduled task interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			reader := bufio.NewReader(os.Stdin)

			name := promptLine(reader, "Task name: ")
			schedule := promptLine(reader, "Cron schedule (e.g. '0 7 * * *'): ")
			promptFile := promptLine(reader, "Prompt file (relative to workspace): ")
			model := promptLine(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
			topicIDStr := promptLine(reader, "Topic ID (optional, run 'leo telegram topics' to discover): ")
			silentStr := promptLine(reader, "Silent mode? [y/N]: ")

			var topicID int
			if topicIDStr != "" {
				fmt.Sscanf(topicIDStr, "%d", &topicID)
			}

			task := config.TaskConfig{
				Schedule:   schedule,
				PromptFile: promptFile,
				Model:      model,
				TopicID:    topicID,
				Enabled:    true,
				Silent:     strings.ToLower(silentStr) == "y",
			}

			if cfg.Tasks == nil {
				cfg.Tasks = make(map[string]config.TaskConfig)
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
			return nil
		},
	}
}

func newTaskRemoveCmd() *cobra.Command {
	return &cobra.Command{
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
		Use:   "history [task-name]",
		Short: "Show task execution history",
		Args:  cobra.MaximumNArgs(1),
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
