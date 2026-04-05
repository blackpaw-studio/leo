package cli

import (
	"github.com/blackpaw-studio/leo/internal/setup"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup wizard",
		Long:  "Set up a new agent with workspace, Telegram integration, and scheduled tasks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setup.Run()
		},
	}
}
