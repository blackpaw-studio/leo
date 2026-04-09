package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
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

			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/task/list", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				var tasks map[string]config.TaskConfig
				if err := json.Unmarshal(resp.Data, &tasks); err != nil {
					return fmt.Errorf("parsing task list: %w", err)
				}
				if len(tasks) == 0 {
					info.Println("No tasks configured.")
					return nil
				}
				fmt.Printf("  %-20s %-18s %-8s %s\n", "NAME", "SCHEDULE", "MODEL", "STATUS")
				for name, task := range tasks {
					status := "disabled"
					if task.Enabled {
						status = "enabled"
					}
					model := cfg.TaskModel(task)
					fmt.Printf("  %-20s %-18s %-8s %s\n", name, task.Schedule, model, status)
				}
				return nil
			}

			if len(cfg.Tasks) == 0 {
				info.Println("No tasks configured.")
				return nil
			}

			fmt.Printf("  %-20s %-18s %-8s %-8s %s\n", "NAME", "SCHEDULE", "MODEL", "STATUS", "LAST RUN")
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
				fmt.Printf("  %-20s %-18s %-8s %-8s %s\n", name, task.Schedule, model, status, lastRun)
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
					result := "ok"
					if e.ExitCode != 0 {
						result = fmt.Sprintf("FAIL (exit %d)", e.ExitCode)
					}
					fmt.Printf("  %s  %s\n", e.RunAt.Local().Format("2006-01-02 15:04:05"), result)
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
				result := "ok"
				if last.ExitCode != 0 {
					result = fmt.Sprintf("FAIL (exit %d)", last.ExitCode)
				}
				fmt.Printf("  %-20s %-5d %s %s\n", task, len(entries),
					last.RunAt.Local().Format("Jan 02 15:04"), result)
			}
			return nil
		},
	}
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
