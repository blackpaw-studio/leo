package cli

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "cron",
		Short:  "Manage scheduled tasks (use 'leo service reload' instead)",
		Hidden: true,
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
		Short: "Alias for 'leo service reload'",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if !daemon.IsRunning(cfg.HomePath) {
				return fmt.Errorf("daemon is not running — start it with 'leo service start' first")
			}

			resp, err := daemon.Send(cfg.HomePath, "POST", "/config/reload", nil)
			if err != nil {
				return fmt.Errorf("sending reload: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("reload failed: %s", resp.Error)
			}

			success.Println("Config reloaded.")
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

			if !daemon.IsRunning(cfg.HomePath) {
				return fmt.Errorf("daemon is not running")
			}

			resp, err := daemon.Send(cfg.HomePath, "POST", "/cron/remove", nil)
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
		Short: "Alias for 'leo task list'",
		RunE: func(cmd *cobra.Command, args []string) error {
			info.Println("Use 'leo task list' to see tasks, or 'leo status' for next scheduled run.")
			return nil
		},
	}
}
