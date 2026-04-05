package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
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

			if len(cfg.Tasks) == 0 {
				info.Println("No tasks configured.")
				return nil
			}

			for name, task := range cfg.Tasks {
				status := "disabled"
				if task.Enabled {
					status = "enabled"
				}

				model := cfg.TaskModel(task)
				fmt.Printf("  %-25s %-20s %-8s %s\n", name, task.Schedule, model, status)
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

			name := prompt(reader, "Task name: ")
			schedule := prompt(reader, "Cron schedule (e.g. '0 7 * * *'): ")
			promptFile := prompt(reader, "Prompt file (relative to workspace): ")
			model := prompt(reader, fmt.Sprintf("Model [%s]: ", cfg.Defaults.Model))
			topic := prompt(reader, "Telegram topic (optional): ")
			silentStr := prompt(reader, "Silent mode? [y/N]: ")

			task := config.TaskConfig{
				Schedule:   schedule,
				Timezone:   "America/New_York",
				PromptFile: promptFile,
				Model:      model,
				Topic:      topic,
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
		Use:   "remove <name>",
		Short: "Remove a task from the config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			name := args[0]
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
		Use:   "enable <name>",
		Short: "Enable a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setTaskEnabled(args[0], true)
		},
	}
}

func newTaskDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a task",
		Args:  cobra.ExactArgs(1),
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

func configPath() (string, error) {
	if cfgFile != "" {
		return cfgFile, nil
	}
	return config.FindConfig("")
}

func prompt(reader *bufio.Reader, label string) string {
	fmt.Print(label)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}
