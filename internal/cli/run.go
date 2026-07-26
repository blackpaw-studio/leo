package cli

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/redact"
	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

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
func taskDryRunEnv(task config.TaskConfig) []envPair {
	// Mirror run.Run's merge order: task.Env is the base layer and leo's own
	// vars win on collision, so a task that sets LEO_CHANNELS in its env
	// shows the value it will actually get — one entry, not two.
	env := make(map[string]string, len(task.Env)+2)
	for k, v := range task.Env {
		env[k] = v
	}
	if len(task.Channels) > 0 {
		env["LEO_CHANNELS"] = strings.Join(task.Channels, ",")
	}
	if len(task.DevChannels) > 0 {
		env["LEO_DEV_CHANNELS"] = strings.Join(task.DevChannels, ",")
	}
	if len(env) == 0 {
		return nil
	}

	pairs := make([]envPair, 0, len(env))
	for _, k := range redact.Keys(env) {
		pairs = append(pairs, envPair{key: k, display: redact.Value(k, env[k])})
	}
	return pairs
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
