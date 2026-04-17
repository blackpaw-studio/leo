package cli

import (
	"bufio"
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the leo binary",
		Long: "Download the latest leo release and replace the running binary.\n\n" +
			"Workspace templates (CLAUDE.md, skills/*.md) re-sync automatically\n" +
			"whenever the service starts — restart the daemon after updating to\n" +
			"pick up any template changes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info.Println("Checking for updates...")

			latest, err := update.CheckLatestVersion()
			if err != nil {
				return fmt.Errorf("checking for updates: %w", err)
			}

			mgr, mgrPath := update.PackageManagerInstall()
			hasUpdate := update.IsNewer(Version, latest)

			// Cases are ordered by priority: up-to-date short-circuits, then
			// --check stays a silent probe regardless of install method, then
			// Homebrew-owned installs delegate to brew, then self-update.
			switch {
			case !hasUpdate:
				if checkOnly {
					success.Printf("Already up to date (%s)\n", Version)
					return nil
				}
				success.Printf("Binary up to date (%s)\n", Version)
				return nil

			case checkOnly:
				info.Printf("Update available: %s → %s\n", Version, latest)
				return nil

			case mgr == update.PackageManagerHomebrew:
				warn.Printf("leo is installed via Homebrew (%s).\n", mgrPath)
				warn.Printf("Update available: %s → %s\n", Version, latest)
				warn.Println("Upgrade with:")
				warn.Println("  brew upgrade blackpaw-studio/tap/leo")
				warn.Println("  leo service restart    # reload the daemon and sync workspace files")
				return nil

			default:
				info.Printf("Downloading leo %s...\n", latest)
				path, err := update.DownloadAndReplace(latest)
				if err != nil {
					return fmt.Errorf("updating binary: %w", err)
				}
				success.Printf("Updated %s to %s\n", path, latest)
			}

			// Offer to restart the daemon so the new binary takes effect —
			// the restart also re-syncs workspace templates.
			cfg, err := loadConfig()
			if err != nil || cfg.IsClientOnly() {
				return nil
			}
			if daemon.IsRunning(cfg.HomePath) {
				reader := bufio.NewReader(os.Stdin)
				if prompt.YesNo(reader, "\nDaemon is running. Restart it now?", true) {
					info.Println("Restarting daemon...")
					if err := service.RestartDaemon(); err != nil {
						return fmt.Errorf("restarting daemon: %w", err)
					}
					success.Println("Daemon restarted")
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false,
		"check if an update is available without installing")

	return cmd
}
