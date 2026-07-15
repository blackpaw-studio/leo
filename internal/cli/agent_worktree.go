package cli

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// newAgentWorktreeCmd spawns a worktree agent branched off an existing
// agent — sugar over spawn --worktree that derives template, repo, and env
// from the source agent's record and works for any git workspace.
func newAgentWorktreeCmd() *cobra.Command {
	var (
		host     string
		name     string
		base     string
		prompt   string
		envPairs []string
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "worktree <agent> <branch>",
		Short: "Spawn a worktree agent branched off an existing agent",
		Long: `Spawn a new agent in a dedicated git worktree derived from an existing
agent: the source agent's template and env are inherited, and its workspace
serves as the git canonical. Works for any agent whose workspace is a git
repository — no owner/repo required. Branching off a worktree agent uses its
canonical repo; pass --base <its-branch> to fork from that branch.

The new agent is named <agent>-<branch-slug> and its worktree lives under
<workspace>/.worktrees/<agent>/<branch-slug>. Clean up with
'leo agent stop <name> --prune'.`,
		Example: `  # Branch the chronicle agent onto an a11y worktree
  leo agent worktree chronicle a11y

  # Fork off a specific ref with an opening prompt
  leo agent worktree chronicle hotfix --base v1.2.0 --prompt "fix the crash"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceAgent, branch := args[0], args[1]
			env, err := parseEnvPairs(envPairs)
			if err != nil {
				return err
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"worktree", sourceAgent, branch}
				if asJSON {
					extra = append(extra, "--json")
				}
				if name != "" {
					extra = append(extra, "--name", name)
				}
				if base != "" {
					extra = append(extra, "--base", base)
				}
				if prompt != "" {
					extra = append(extra, "--prompt", prompt)
				}
				for _, p := range envPairs {
					extra = append(extra, "--env", p)
				}
				return runRemote(res, extra)
			}

			rec, err := daemon.AgentSpawn(cmd.Context(), cfg.HomePath, daemon.AgentSpawnRequest{
				FromAgent: sourceAgent,
				Branch:    branch,
				Base:      base,
				Name:      name,
				Prompt:    prompt,
				Env:       env,
			})
			if err != nil {
				return fmt.Errorf("spawning worktree agent: %w", err)
			}
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rec)
			}
			fmt.Fprintf(agentStdout, "spawned %s (branch: %s, worktree: %s)\n", rec.Name, rec.Branch, rec.Workspace)
			fmt.Fprintf(agentStdout, "attach with: leo agent attach %s\n", rec.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the spawned agent record as JSON")
	cmd.Flags().StringVar(&name, "name", "", "override the derived agent name")
	cmd.Flags().StringVar(&base, "base", "", "base ref for new branches (defaults to origin HEAD, or HEAD for remoteless repos)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "opening prompt delivered as the agent's first interactive turn")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "extra env var as KEY=VALUE (repeatable); overrides inherited env on collision")
	return cmd
}
