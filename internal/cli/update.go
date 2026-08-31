package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/update"
	"github.com/spf13/cobra"
)

// allowUnsignedFromEnv returns true when LEO_ALLOW_UNSIGNED_RELEASE is
// set to a truthy value (1, true, yes, on, …). Unset, empty, or
// falsey values (0, false, no, off) all return false. Using ParseBool
// avoids the previous "any non-empty value enables fallback" footgun
// where LEO_ALLOW_UNSIGNED_RELEASE=false was interpreted as "enable
// fallback".
func allowUnsignedFromEnv() bool {
	raw := os.Getenv(update.UnsignedReleaseEnv)
	if raw == "" {
		return false
	}
	// strconv.ParseBool accepts 1/t/T/true/TRUE/True/0/f/F/false/FALSE/False.
	// Extend to the other common "truthy/falsy" words explicitly so
	// users typing yes/on/no/off aren't surprised.
	if v, err := strconv.ParseBool(raw); err == nil {
		return v
	}
	switch raw {
	case "yes", "YES", "Yes", "on", "ON", "On":
		return true
	case "no", "NO", "No", "off", "OFF", "Off":
		return false
	}
	// Unrecognised values fall through to strict-mode default: a typo
	// should not silently disable signature verification.
	return false
}

func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	var allowUnsigned bool
	var prNumber int
	var pinnedVersion string
	var unstable bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the leo binary",
		Long: "Download the latest leo release and replace the running binary.\n\n" +
			"Workspace templates (CLAUDE.md, skills/*.md) re-sync automatically\n" +
			"whenever the service starts — restart the daemon after updating to\n" +
			"pick up any template changes.\n\n" +
			"Pass --pr <n> to install the most recent successful PR build\n" +
			"(uploaded by the prerelease workflow), or --version pr-<n>-<sha>\n" +
			"to pin to an exact PR build. Pass --unstable to install the most\n" +
			"recent passing build of main, or --version main-<sha> to pin to an\n" +
			"exact main build. All of these need a GitHub token; the command\n" +
			"checks $LEO_GITHUB_TOKEN, then $GH_TOKEN, then $GITHUB_TOKEN,\n" +
			"and finally falls back to the gh CLI (`gh auth token`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			selected := 0
			for _, name := range []string{"pr", "unstable", "version"} {
				if cmd.Flags().Changed(name) {
					selected++
				}
			}
			if selected > 1 {
				return fmt.Errorf("--pr, --unstable, and --version are mutually exclusive")
			}
			if prNumber > 0 {
				return runPrereleaseUpdateByPR(prNumber, allowUnsigned)
			}
			if unstable {
				return runUnstableUpdate(allowUnsigned)
			}
			if update.IsPrereleaseVersion(pinnedVersion) {
				return runPrereleaseUpdateByVersion(pinnedVersion, allowUnsigned)
			}
			if update.IsMainVersion(pinnedVersion) {
				return runUnstableUpdateByVersion(pinnedVersion, allowUnsigned)
			}
			if pinnedVersion != "" {
				return fmt.Errorf("--version currently supports prerelease tags only (pr-<n>-<sha> or main-<sha>); to install a tagged release, omit --version and let leo find the latest")
			}

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

			case mgr == update.PackageManagerHomebrewCask:
				warn.Printf("leo is installed via Homebrew (%s).\n", mgrPath)
				warn.Printf("Update available: %s → %s\n", Version, latest)
				warn.Println("Upgrade with:")
				warn.Println("  brew upgrade --cask blackpaw-studio/tap/leo")
				warn.Println("  leo service restart    # reload the daemon and sync workspace files")
				return nil

			default:
				info.Printf("Downloading leo %s...\n", latest)
				opts := update.UpdateOptions{
					// Allow fallback when the flag is passed explicitly OR when
					// the env var is set to a truthy value — both are equivalent
					// escape hatches for the rollout window where old releases
					// have no sig.
					AllowUnsigned: allowUnsigned || allowUnsignedFromEnv(),
					Warn: func(format string, args ...any) {
						warn.Printf(format+"\n", args...)
					},
				}
				path, err := update.DownloadAndReplaceWithOptions(latest, opts)
				if err != nil {
					return fmt.Errorf("updating binary: %w", err)
				}
				success.Printf("Updated %s to %s\n", path, latest)
			}

			// Offer to restart the daemon so the new binary takes effect —
			// the restart also re-syncs workspace templates.
			return maybeRestartDaemon()
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false,
		"check if an update is available without installing")
	cmd.Flags().BoolVar(&allowUnsigned, "allow-unsigned", false,
		"permit updating from a release without a cosign signature (SHA-256 only)")
	cmd.Flags().IntVar(&prNumber, "pr", 0,
		"install the most recent successful PR build (requires a GitHub token)")
	cmd.Flags().BoolVar(&unstable, "unstable", false,
		"install the most recent passing main build (requires a GitHub token)")
	cmd.Flags().StringVar(&pinnedVersion, "version", "",
		"pin to a specific version, e.g. --version pr-42-a1b2c3d or --version main-a1b2c3d")
	// Advertise the env-var equivalent without cluttering --help.
	_ = cmd.Flags().MarkHidden("allow-unsigned")

	return cmd
}

// runPrereleaseUpdateByPR resolves the latest passing prerelease run on
// the given PR, then verifies + installs its artifact. Shared restart
// prompt at the end mirrors the stable path.
func runPrereleaseUpdateByPR(prNumber int, allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Printf("Installing latest prerelease build for PR #%d...\n", prNumber)
	path, version, err := update.DownloadAndReplacePR(context.Background(), prNumber, opts)
	if err != nil {
		return fmt.Errorf("installing PR #%d build: %w", prNumber, err)
	}
	success.Printf("Updated %s to %s\n", path, version)
	return maybeRestartDaemon()
}

// runPrereleaseUpdateByVersion installs a specific pr-<n>-<sha> build.
func runPrereleaseUpdateByVersion(version string, allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Printf("Installing prerelease build %s...\n", version)
	path, installedVersion, err := update.DownloadAndReplacePRVersion(context.Background(), version, opts)
	if err != nil {
		return fmt.Errorf("installing %s: %w", version, err)
	}
	success.Printf("Updated %s to %s\n", path, installedVersion)
	return maybeRestartDaemon()
}

// runUnstableUpdate installs the latest passing main build.
func runUnstableUpdate(allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Println("Installing latest main build...")
	path, version, err := update.DownloadAndReplaceMain(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("installing main build: %w", err)
	}
	success.Printf("Updated %s to %s\n", path, version)
	return maybeRestartDaemon()
}

// runUnstableUpdateByVersion installs a specific main-<sha> build.
func runUnstableUpdateByVersion(version string, allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Printf("Installing main build %s...\n", version)
	path, installedVersion, err := update.DownloadAndReplaceMainVersion(context.Background(), version, opts)
	if err != nil {
		return fmt.Errorf("installing %s: %w", version, err)
	}
	success.Printf("Updated %s to %s\n", path, installedVersion)
	return maybeRestartDaemon()
}

func prereleaseOptions(allowUnsigned bool) update.PrereleaseOptions {
	return update.PrereleaseOptions{
		AllowUnsigned: allowUnsigned || allowUnsignedFromEnv(),
		Warn: func(format string, args ...any) {
			warn.Printf(format+"\n", args...)
		},
	}
}

// maybeRestartDaemon offers to bounce the daemon after a binary swap so
// the new binary actually serves new requests. Same prompt the stable
// update path uses — factored out so both paths stay in sync.
func maybeRestartDaemon() error {
	cfg, err := loadConfig()
	if err != nil || cfg.IsClientOnly() {
		return nil
	}
	if !daemon.IsRunning(cfg.HomePath) {
		return nil
	}
	// Without a terminal there is nobody to answer the prompt, and the
	// empty read would silently accept the "yes" default — restarting the
	// daemon out from under an unattended update. Leave the restart to
	// the operator instead.
	if !prompt.IsInteractive() {
		info.Println("\nDaemon is still running the previous binary. Restart it when ready with: leo service restart")
		return nil
	}
	reader := bufio.NewReader(os.Stdin)
	if !prompt.YesNo(reader, "\nDaemon is running. Restart it now?", true) {
		return nil
	}
	info.Println("Restarting daemon...")
	if err := service.RestartDaemon(cfg.HomePath); err != nil {
		return fmt.Errorf("restarting daemon: %w", err)
	}
	success.Println("Daemon restarted")

	// Only now, with the new binary serving, is it worth asking which agents
	// are still running the old wiring: restoring the daemon respawns agents
	// from their stored args/env, so "daemon restarted" does not mean the
	// update reached them. The drift check itself runs inside the daemon, so
	// asking a pre-restart daemon would answer with the old binary's logic.
	maybeRestartStaleAgents(context.Background(), cfg.HomePath)
	return nil
}
