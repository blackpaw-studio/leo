package cli

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage cron entries",
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
		Short: "Install all enabled tasks to system crontab",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/install", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Println("Cron entries installed (via daemon).")
				return nil
			}

			leoPath, err := leoExecutablePath()
			if err != nil {
				return fmt.Errorf("finding leo binary: %w", err)
			}

			if err := cron.Install(cfg, leoPath); err != nil {
				return err
			}

			success.Println("Cron entries installed.")
			return nil
		},
	}
}

func newCronRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Remove all leo-managed cron entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/remove", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Println("Cron entries removed (via daemon).")
				return nil
			}

			if err := cron.Remove(cfg); err != nil {
				return err
			}

			success.Println("Cron entries removed.")
			return nil
		},
	}
}

func newCronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show installed schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "GET", "/cron/list", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				var data struct {
					Entries string `json:"entries"`
				}
				json.Unmarshal(resp.Data, &data)
				if data.Entries == "" {
					warn.Println("No leo cron entries found.")
				} else {
					fmt.Println(data.Entries)
				}
				return nil
			}

			block, err := cron.List(cfg)
			if err != nil {
				return err
			}

			if block == "" {
				warn.Println("No leo cron entries found.")
				return nil
			}

			fmt.Println(block)
			return nil
		},
	}
}

