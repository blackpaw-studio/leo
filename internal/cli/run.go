package cli

import (
	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <task>",
		Short: "Run a scheduled task once",
		Long:  "Execute a scheduled task. Used by cron or for manual testing.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			taskName := args[0]
			return run.Run(cfg, taskName)
		},
	}
}
