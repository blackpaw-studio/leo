package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage scheduled tasks",
	}

	cmd.AddCommand(
		newCronInstallCmd(),
		newCronRemoveCmd(),
		newCronListCmd(),
	)

	return cmd
}

func newCronInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register all enabled task schedules with the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if !daemon.IsRunning(cfg.Agent.Workspace) {
				return fmt.Errorf("daemon is not running — start it with 'leo chat start' first")
			}

			resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/install", nil)
			if err != nil {
				return fmt.Errorf("sending to daemon: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("daemon error: %s", resp.Error)
			}

			success.Println("Schedules registered with daemon.")
			return nil
		},
	}
}

func newCronRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Unregister all scheduled tasks from the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if !daemon.IsRunning(cfg.Agent.Workspace) {
				return fmt.Errorf("daemon is not running — start it with 'leo chat start' first")
			}

			resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/remove", nil)
			if err != nil {
				return fmt.Errorf("sending to daemon: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("daemon error: %s", resp.Error)
			}

			success.Println("Schedules unregistered.")
			return nil
		},
	}
}

func newCronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show scheduled tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "GET", "/cron/list", nil)
				if err == nil && resp.OK {
					var entries []cron.EntryInfo
					if err := json.Unmarshal(resp.Data, &entries); err == nil {
						if len(entries) == 0 {
							warn.Println("No schedules registered.")
						} else {
							for _, e := range entries {
								fmt.Printf("  %-25s  %-20s  next: %s\n",
									e.Name, e.Schedule, e.Next.Format(time.RFC3339))
							}
						}
						return nil
					}
				}
			}

			// Fallback: show config-defined tasks
			warn.Println("Daemon not running — showing config-defined tasks:")
			for name, task := range cfg.Tasks {
				status := "disabled"
				if task.Enabled {
					status = "enabled"
				}
				fmt.Printf("  %-25s  %-20s  [%s]\n", name, task.Schedule, status)
			}
			return nil
		},
	}
}
