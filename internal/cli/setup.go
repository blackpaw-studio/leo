package cli

import (
	"github.com/blackpaw-studio/leo/internal/setup"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup wizard",
		Long:  "Set up a new workspace and scheduled tasks. Channel plugins (Telegram, Slack, etc.) are installed separately via `claude plugin install`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setup.Run()
		},
	}
}
