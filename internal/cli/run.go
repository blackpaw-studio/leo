package cli

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:               "run <task>",
		Short:             "Run a scheduled task once",
		Long:              "Execute a scheduled task. Used by cron or for manual testing.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			taskName := args[0]
			sessions := session.NewStore(cfg.HomePath)

			if dryRun {
				prompt, cliArgs, err := run.Preview(cfg, taskName, sessions)
				if err != nil {
					return err
				}
				info.Println("Command:")
				fmt.Printf("  claude %s\n\n", strings.Join(cliArgs, " "))
				info.Println("Assembled prompt:")
				fmt.Println(prompt)

				if task, ok := cfg.Tasks[taskName]; ok && len(task.Channels) > 0 {
					fmt.Println()
					info.Println("Channels (exported as LEO_CHANNELS):")
					for _, ch := range task.Channels {
						fmt.Printf("  - %s\n", ch)
					}
				}

				return nil
			}

			return run.Run(cfg, taskName, sessions)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show assembled prompt and args without executing")

	return cmd
}

func completeTaskNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for name := range cfg.Tasks {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
