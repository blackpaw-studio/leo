package cli

import (
	"github.com/blackpaw-studio/leo/internal/migrate"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate from OpenClaw",
		Long:  "Migrate an existing OpenClaw installation to Leo.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return migrate.Run()
		},
	}
}
