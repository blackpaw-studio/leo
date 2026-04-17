package cli

import (
	"github.com/spf13/cobra"
)

// newCronCmd returns a hidden, deprecated stub for the old `leo cron`
// command group. The install/list/remove subcommands have been retired —
// `leo service reload` handles (re)loading the cron schedule from the
// config, and `leo task list` shows the configured tasks. The parent is
// kept (returning a no-op command) so root.go wiring remains stable; we
// can drop it entirely in a follow-up once all user docs have been updated.
func newCronCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cron",
		Short:  "Deprecated — use 'leo service reload' and 'leo task list'",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			info.Println("The 'leo cron' subcommands have been retired.")
			info.Println("  - To refresh scheduled tasks after config changes: leo service reload")
			info.Println("  - To list configured tasks:                         leo task list")
			return nil
		},
	}
}
