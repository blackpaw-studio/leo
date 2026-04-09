package cli

import (
	"github.com/spf13/cobra"
)

var (
	cfgFile   string
	workspace string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leo",
		Short: "Manage a persistent Claude Code assistant",
		Long:  "Leo sets up and manages a persistent Claude Code session with Telegram, Remote Control, and scheduled tasks.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to leo.yaml (default: auto-detect)")
	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "", "workspace directory (default: from config)")

	cmd.AddCommand(
		newVersionCmd(),
		newOnboardCmd(),
		newRunCmd(),
		newServiceCmd(),
		newCronCmd(),
		newTaskCmd(),
		newSetupCmd(),
		newMigrateCmd(),
		newValidateCmd(),
		newUpdateCmd(),
		newTelegramCmd(),
		newSessionCmd(),
		newCompletionCmd(),
		newStatusCmd(),
		newConfigCmd(),
	)

	return cmd
}

func Execute() error {
	return newRootCmd().Execute()
}
