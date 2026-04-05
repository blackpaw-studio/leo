package cli

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
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

			if dryRun {
				prompt, cliArgs, err := run.Preview(cfg, taskName)
				if err != nil {
					return err
				}
				info.Println("Command:")
				fmt.Printf("  claude %s\n\n", strings.Join(cliArgs, " "))
				info.Println("Assembled prompt:")
				fmt.Println(prompt)
				return nil
			}

			return run.Run(cfg, taskName)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show assembled prompt and args without executing")

	return cmd
}
