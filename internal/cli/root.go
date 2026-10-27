package cli

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

var cfgFile string

// rootLong is shown by `leo --help` and by `leo` with no subcommand.
const rootLong = `Leo supervises Claude Code agents and scheduled tasks.

Two core primitives:
  - Agents     on-demand ephemeral sessions spawned from templates (` + "`leo agent`" + `);
               persistent tasks deliver into agent tmux sessions
  - Tasks      cron-scheduled prompts, oneshot or persistent (` + "`leo run`" + `)

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
		newAgentCmd(),
		newConsultCmd(),
		newDispatchCmd(),
		newHostCmd(),
		newAPIClientCmd(),
		newPairCmd(),
		newDevicesCmd(),
		newAttachCmd(),
		newCronCmd(),
		newTaskCmd(),
		newTemplateCmd(),
		newDelegationCmd(),
		newSyncCmd(),
		newSetupCmd(),
		newValidateCmd(),
		newUpdateCmd(),
		newCompletionCmd(),
		newStatusCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newLogsCmd(),
		newMCPServerCmd(),
		newChannelsCmd(),
		newWebCmd(),
		newInternalCmd(),
		newBridgeCmd(),
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
		_, _ = io.WriteString(out, "  leo status    check agents, tasks, and daemon health\n")
		_, _ = io.WriteString(out, "  leo --help    list all commands\n")
	} else {
		_, _ = io.WriteString(out, "  leo setup     create a leo.yaml and take the guided tour\n")
		_, _ = io.WriteString(out, "  leo --help    list all commands\n")
	}
}

func Execute() error {
	return executeRoot(newRootCmd(), os.Args[1:])
}

// jsonAnnotation marks a command whose --json mode promises exactly one JSON
// object on stdout for every outcome, failures included.
const jsonAnnotation = "leo/json"

// markJSON flags cmd as keeping the --json promise.
func markJSON(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[jsonAnnotation] = "true"
	return cmd
}

// executeRoot runs root with args. A --json command that fails before its own
// code runs (an unknown flag, a wrong argument count, mutually exclusive
// flags) still answers with {error: "invalid_args", message}: cobra raises
// those, so only a wrapper around the whole run can keep the JSON rule.
func executeRoot(root *cobra.Command, args []string) error {
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	if err == nil || errors.Is(err, errJSONReported) {
		return err
	}
	if cmd == nil || cmd.Annotations[jsonAnnotation] != "true" || !jsonRequested(args) {
		return err
	}
	if werr := writeJSON(root.OutOrStdout(), jsonFailure{Error: "invalid_args", Message: err.Error()}); werr != nil {
		return werr
	}
	return errJSONReported
}

// jsonRequested reports whether args ask for --json (before any "--").
func jsonRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" {
			return true
		}
		if v, ok := strings.CutPrefix(a, "--json="); ok {
			on, err := strconv.ParseBool(v)
			return err == nil && on
		}
	}
	return false
}
