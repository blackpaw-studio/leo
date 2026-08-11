package cli

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// newAgentSetTemplateCmd registers `leo agent set-template <name> <template>`.
//
// Permission gating is deliberately doubled up: a switch stops the agent and
// launches it on another template, so it needs both the disruption permission
// every bounce needs (leo_stop_agent) and the spawn permission for the TARGET
// template. Gating only the former would let a template restricted from
// spawning `codex` reach codex anyway by switching an agent into it.
func newAgentSetTemplateCmd() *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set-template <name> <template>",
		Short: "Point an agent at a different template, keeping its workspace",
		Long: `Re-point a running or suspended agent at a different template. The agent
keeps its name, workspace, and git worktree; its harness, model, permissions,
env, and other wiring are rebuilt from the target template.

Conversations are per-template. Switching away files the current session under
the template being left, and switching back hands that conversation to it
again — so you can move a project between templates (a different harness, a
different model, a tighter permission set) without losing where you were.
A template you have not used on this agent before starts fresh.

The agent's name is left alone, even when it embeds the old template name, so
tmux sessions, channel routing, and anything else holding the name keep
working. Rename it yourself with 'leo agent rename' if you want it to match.

A running agent is stopped and respawned. A suspended agent is re-pointed in
place and comes up on the new template at its next resume. Stopped agents, and
agents backing a 'runtime: persistent' task, are refused.`,
		Example: `  # Try this project under codex, keeping the workspace
  leo agent set-template leo-coding-owner-fetch codex

  # ...and back again, resuming the claude conversation you left
  leo agent set-template leo-coding-owner-fetch coding`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeAgentThenTemplate,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, template := args[0], args[1]
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			if err := gateSpawnTemplate(cmd, template); err != nil {
				return err
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"set-template", name, template}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemote(res, extra)
			}

			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			result, err := daemon.AgentSwitchTemplate(cmd.Context(), cfg.HomePath, resolved.Name, template)
			if err != nil {
				return fmt.Errorf("switching template: %w", err)
			}

			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			fmt.Fprint(agentStdout, formatSwitchResult(result))
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the switch result as JSON")
	return cmd
}

// formatSwitchResult renders the human summary. "set-template" reads lighter
// than the command acts — it bounces the process and swaps the live
// conversation — so the output always states what happened to both.
func formatSwitchResult(r agent.SwitchResult) string {
	if r.Unchanged {
		return fmt.Sprintf("%s is already on template %s; nothing changed\n", r.Name, r.ToTemplate)
	}

	line := fmt.Sprintf("%s: %s → %s", r.Name, r.FromTemplate, r.ToTemplate)
	if r.FromHarness != r.ToHarness {
		line += fmt.Sprintf(" (%s → %s)", r.FromHarness, r.ToHarness)
	}

	var outcome string
	switch {
	case r.Status == "suspended" && r.Resumed:
		outcome = "still suspended; resumes this template's previous session when it wakes"
	case r.Status == "suspended":
		outcome = "still suspended; starts a new session when it wakes"
	case r.Resumed:
		outcome = "respawned, resumed this template's previous session"
	default:
		outcome = "respawned on a new session"
	}
	return line + "\n" + outcome + "\n"
}

// completeAgentThenTemplate completes agent names in the first position and
// template names in the second, so `leo agent set-template <tab> <tab>` fills
// in both halves.
func completeAgentThenTemplate(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeAgentNames(cmd, args, toComplete)
	case 1:
		return completeTemplateNames(cmd, nil, toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}
