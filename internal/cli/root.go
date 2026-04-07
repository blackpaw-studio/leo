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
		Short: "Manage Claude Code agents as persistent personal assistants",
		Long:  "Leo sets up and manages Claude Code agents with Telegram integration and cron scheduling.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to leo.yaml (default: auto-detect)")
	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "", "workspace directory (default: from config)")

	cmd.AddCommand(
		newVersionCmd(),
		newOnboardCmd(),
		newRunCmd(),
		newChatCmd(),
		newCronCmd(),
		newTaskCmd(),
		newSetupCmd(),
		newMigrateCmd(),
		newValidateCmd(),
		newUpdateCmd(),
		newTelegramCmd(),
	)

	return cmd
}

func Execute() error {
	return newRootCmd().Execute()
}
