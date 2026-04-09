package cli

import (
	"github.com/blackpaw-studio/leo/internal/onboard"
	"github.com/spf13/cobra"
)

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Guided first-time setup",
		Long:  "Walk through prerequisites, detect existing installations, and set up a new workspace or migrate from OpenClaw.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return onboard.Run()
		},
	}
}
