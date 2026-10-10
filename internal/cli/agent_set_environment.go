package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// newAgentSetEnvironmentCmd registers `leo agent set-environment <name> [envs]`.
func newAgentSetEnvironmentCmd() *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set-environment <name> [environments]",
		Short: "Change the named environments an agent runs under, restarting it",
		Long: `Change the ordered list of named environments (from the 'environments:' block
in leo.yaml) an agent runs under — typically to move it to another account by
way of CLAUDE_CONFIG_DIR or CODEX_HOME. The list is comma-separated and merged
left to right; later names win when two set the same key.

The agent's environment is rebuilt from current config (keys of the old
environments do not linger) and a running agent is stopped and respawned with
its conversation resumed. A dormant (stopped) agent is rewritten in place and
comes up on the new environments the next time it starts.

Omit the list to clear the override, so the template's default environments
apply again. Resuming across accounts needs 'projects' shared between the
accounts' config dirs — see docs/configuration/environments.md.

Agents with no persisted record, and agents backing a 'runtime: persistent'
task, are refused.`,
		Example: `  # Move an agent to the second Claude account
  leo agent set-environment leo-coding-owner-fetch acct-b

  # Layer two environments (acct-b wins on shared keys)
  leo agent set-environment leo-coding-owner-fetch base,acct-b

  # Back to the template default
  leo agent set-environment leo-coding-owner-fetch`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeAgentThenEnvironment,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var names []string
			if len(args) == 2 {
				names = parseEnvironmentList(args[1])
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				// The target's template is only known to the remote daemon, so
				// the template half of the gate runs there; the tool denial
				// applies here too, before anything leaves the machine.
				if err := gateToolFor(cmd.CommandPath(), "leo_set_agent_environments"); err != nil {
					return err
				}
				extra := []string{"set-environment", name}
				if len(args) == 2 {
					extra = append(extra, args[1])
				}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemote(res, extra)
			}

			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			if err := gateEnvironmentSwitch(cmd.CommandPath(), resolved.Template); err != nil {
				return err
			}
			result, err := daemon.AgentSetEnvironment(cmd.Context(), cfg.HomePath, resolved.Name, names)
			if err != nil {
				return fmt.Errorf("setting environments: %w", err)
			}
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			fmt.Fprintln(agentStdout, formatSetEnvironmentsResult(result))
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the result as JSON")
	return cmd
}

// parseEnvironmentList splits a comma-separated environment list, dropping
// blanks. An empty input yields nil ("no override").
func parseEnvironmentList(raw string) []string {
	var out []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// formatSetEnvironmentsResult renders the human summary, without a trailing
// newline. It states both what the agent now runs under and what happened to
// the process and conversation.
func formatSetEnvironmentsResult(r agent.SetEnvironmentsResult) string {
	list := strings.Join(r.Effective, ", ")
	if list == "" {
		list = "none"
	}
	if r.Unchanged {
		return fmt.Sprintf("%s already runs under %s; nothing changed", r.Name, list)
	}
	head := fmt.Sprintf("%s now runs under: %s", r.Name, list)
	if len(r.To) == 0 {
		head = fmt.Sprintf("%s: override cleared; template default applies (%s)", r.Name, list)
	}
	if r.Status == "stopped" {
		return head + "\nstill stopped; takes effect on next start"
	}
	return head + "\nrestarted, session resumed"
}

// completeAgentThenEnvironment completes agent names in the first position and
// environment names (comma-aware) in the second.
func completeAgentThenEnvironment(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeAgentNames(cmd, args, toComplete)
	case 1:
		return completeEnvironmentNames(toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeEnvironmentNames completes the last comma-separated element of
// toComplete against the configured environment names.
func completeEnvironmentNames(toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	prefix := ""
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix = toComplete[:i+1]
	}
	var out []string
	for name := range cfg.Environments {
		out = append(out, prefix+name)
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
