package cli

import (
	"github.com/spf13/cobra"
)

var cfgFile string

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "leo",
		Short:         "Manage a persistent Claude Code assistant",
		Long:          "Leo sets up and manages persistent Claude Code sessions with Telegram, Remote Control, and scheduled tasks.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to leo.yaml (default: auto-detect)")

	cmd.AddCommand(
		newVersionCmd(),
		newOnboardCmd(),
		newRunCmd(),
		newServiceCmd(),
		newProcessCmd(),
		newCronCmd(),
		newTaskCmd(),
		newSetupCmd(),
		newValidateCmd(),
		newUpdateCmd(),
		newTelegramCmd(),
		newSessionCmd(),
		newCompletionCmd(),
		newStatusCmd(),
		newConfigCmd(),
		newLogsCmd(),
	)

	return cmd
}

func Execute() error {
	return newRootCmd().Execute()
}
