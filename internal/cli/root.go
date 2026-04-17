package cli

import (
	"io"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

var cfgFile string

// rootLong is shown by `leo --help` and by `leo` with no subcommand.
const rootLong = `Leo supervises persistent Claude Code sessions and scheduled tasks.

Three core primitives:
  - Processes  long-running Claude sessions managed under ` + "`leo service`" + `
  - Agents     on-demand ephemeral sessions spawned from templates (` + "`leo agent`" + `)
  - Tasks      cron-scheduled prompts that run on their own (` + "`leo run`" + `)

Channels (Telegram, Slack, webhook, etc.) are provided by separately-installed
Claude Code plugins — Leo only knows them as opaque plugin IDs.`

// findConfigFn is a test seam for no-args narration.
var findConfigFn = config.FindConfig

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "leo",
		Short:         "Manage a persistent Claude Code assistant",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, args []string) {
			// No subcommand: print Long + a short "Getting started" hint.
			runRootNoArgs(cmd.OutOrStdout(), cfgFile)
		},
	}

	// Mirror `leo version` output exactly for `leo --version` / `leo -v`.
	cmd.SetVersionTemplate("leo {{.Version}}\n")

	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to leo.yaml (default: auto-detect)")

	cmd.AddCommand(
		newVersionCmd(),
		newOnboardCmd(),
		newRunCmd(),
		newServiceCmd(),
		newProcessCmd(),
		newAgentCmd(),
		newAttachCmd(),
		newCronCmd(),
		newTaskCmd(),
		newTemplateCmd(),
		newSetupCmd(),
		newValidateCmd(),
		newUpdateCmd(),
		newSessionCmd(),
		newCompletionCmd(),
		newStatusCmd(),
		newConfigCmd(),
		newLogsCmd(),
		newMCPServerCmd(),
		newChannelsCmd(),
	)

	return cmd
}

// runRootNoArgs prints the Long description and a contextual "Getting started"
// hint that depends on whether a leo.yaml is already discoverable.
func runRootNoArgs(out io.Writer, cfgPath string) {
	_, _ = io.WriteString(out, rootLong)
	_, _ = io.WriteString(out, "\n\nGetting started:\n")

	hasConfig := cfgPath != ""
	if !hasConfig {
		if _, err := findConfigFn(""); err == nil {
			hasConfig = true
		}
	}

	if hasConfig {
		_, _ = io.WriteString(out, "  leo status    check processes, tasks, and daemon health\n")
		_, _ = io.WriteString(out, "  leo --help    list all commands\n")
	} else {
		_, _ = io.WriteString(out, "  leo setup     create a leo.yaml and take the guided tour\n")
		_, _ = io.WriteString(out, "  leo --help    list all commands\n")
	}
}

func Execute() error {
	return newRootCmd().Execute()
}
