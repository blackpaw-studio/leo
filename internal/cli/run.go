package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

// redactedKeyTokens are substrings in env var keys that trigger value
// redaction. Case-insensitive match.
var redactedKeyTokens = []string{"SECRET", "TOKEN", "KEY", "PASSWORD"}

func newRunCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:               "run <task>",
		Short:             "Run a scheduled task once",
		Long:              "Execute a scheduled task. Used by cron or for manual testing.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			taskName := args[0]
			sessions := session.NewStore(cfg.HomePath)

			if dryRun {
				prompt, cliArgs, err := run.Preview(cfg, taskName, sessions)
				if err != nil {
					return err
				}
				info.Println("Command:")
				fmt.Printf("  claude %s\n\n", strings.Join(cliArgs, " "))
				info.Println("Assembled prompt:")
				fmt.Println(prompt)

				task, ok := cfg.Tasks[taskName]
				if ok {
					if len(task.Channels) > 0 {
						fmt.Println()
						info.Println("Channels (exported as LEO_CHANNELS):")
						for _, ch := range task.Channels {
							fmt.Printf("  - %s\n", ch)
						}
					}

					// Show the env vars that would be set on the child claude process.
					envPairs := taskDryRunEnv(task)
					if len(envPairs) > 0 {
						fmt.Println()
						info.Println("Environment:")
						for _, p := range envPairs {
							fmt.Printf("  %s=%s\n", p.key, p.display)
						}
					}
				}

				return nil
			}

			return run.Run(cfg, taskName, sessions)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show assembled prompt and args without executing")

	return cmd
}

// envPair is an internal key/display-value pair for dry-run output. The
// display value may be redacted; the raw value is never stored.
type envPair struct {
	key     string
	display string
}

// taskDryRunEnv returns the env vars that would be exported to the child
// claude process for a dry-run, redacting sensitive values. Sorted by key for
// deterministic output.
//
// TaskConfig does not yet have an Env field. When one is added, iterate it
// here and route each entry through redactValue so keys matching the
// redaction tokens (SECRET/TOKEN/KEY/PASSWORD) are masked:
//
//	for k, v := range task.Env {
//	    pairs = append(pairs, envPair{key: k, display: redactValue(k, v)})
//	}
func taskDryRunEnv(task config.TaskConfig) []envPair {
	var pairs []envPair

	if len(task.Channels) > 0 {
		pairs = append(pairs, envPair{key: "LEO_CHANNELS", display: redactValue("LEO_CHANNELS", strings.Join(task.Channels, ","))})
	}
	if len(task.DevChannels) > 0 {
		pairs = append(pairs, envPair{key: "LEO_DEV_CHANNELS", display: redactValue("LEO_DEV_CHANNELS", strings.Join(task.DevChannels, ","))})
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	return pairs
}

// shouldRedactEnvKey reports whether an env var value should be displayed as
// <redacted> based on its key. Matches SECRET, TOKEN, KEY, PASSWORD as
// case-insensitive substrings.
func shouldRedactEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, tok := range redactedKeyTokens {
		if strings.Contains(upper, tok) {
			return true
		}
	}
	return false
}

// redactValue returns the displayable value for an env var: <redacted> if the
// key matches any redaction token, otherwise the original value.
func redactValue(key, value string) string {
	if shouldRedactEnvKey(key) {
		return "<redacted>"
	}
	return value
}

func completeTaskNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for name := range cfg.Tasks {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
