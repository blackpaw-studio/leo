package cli

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/cron"
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

