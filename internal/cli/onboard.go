package cli

import (
	"github.com/blackpaw-studio/leo/internal/setup"
	"github.com/spf13/cobra"
)

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "onboard",
		Short:  "Alias for 'leo setup'",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setup.Run()
		},
	}
}
