package cli

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var workspaceOnly bool
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update leo binary and refresh workspace files",
		Long:  "Download the latest leo release and update workspace template files (CLAUDE.md, skills/*.md).",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Binary update (unless --workspace-only)
			if !workspaceOnly {
				info.Println("Checking for updates...")

				latest, err := update.CheckLatestVersion()
				if err != nil {
					return fmt.Errorf("checking for updates: %w", err)
				}

				if update.IsNewer(Version, latest) {
					if checkOnly {
						info.Printf("Update available: %s → %s\n", Version, latest)
						return nil
					}

					info.Printf("Downloading leo %s...\n", latest)
					path, err := update.DownloadAndReplace(latest)
					if err != nil {
						return fmt.Errorf("updating binary: %w", err)
					}
					success.Printf("Updated %s to %s\n", path, latest)
				} else {
					if checkOnly {
						success.Printf("Already up to date (%s)\n", Version)
						return nil
					}
					success.Printf("Binary up to date (%s)\n", Version)
				}
			}

			if checkOnly {
				return nil
			}

			// Workspace refresh
			cfg, err := loadConfig()
			if err != nil {
				warn.Printf("Skipping workspace refresh: %v\n", err)
				return nil
			}

			info.Println("Refreshing workspace files...")
			written, err := update.RefreshWorkspace(cfg.Agent.Name, cfg.Agent.Workspace)
			if err != nil {
				return fmt.Errorf("refreshing workspace: %w", err)
			}

			for _, path := range written {
				info.Printf("  Updated %s\n", path)
			}
			success.Printf("Refreshed %d file(s)\n", len(written))

			return nil
		},
	}

	cmd.Flags().BoolVar(&workspaceOnly, "workspace-only", false,
		"skip binary update, only refresh workspace files")
	cmd.Flags().BoolVar(&checkOnly, "check", false,
		"check if an update is available without installing")

	return cmd
}
