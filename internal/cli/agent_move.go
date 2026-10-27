package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentmove"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

// movePollInterval paces polls of a running move or copy job.
const movePollInterval = 500 * time.Millisecond

// Testability seams.
var (
	moveStartFn     = daemon.StartMove
	moveJobFn       = daemon.MoveJob
	moveReconcileFn = daemon.ReconcileMove
	moveAbandonFn   = daemon.AbandonMove
	movePollTick    = func() <-chan time.Time { return time.After(movePollInterval) }
)

func newAgentMoveCmd() *cobra.Command {
	var host, to string
	var force, asJSON, reconcile, abandon, assumeYes bool
	cmd := &cobra.Command{
		Use:   "move <name> --to <host>",
		Short: "Hand an agent off to another leo host",
		Long: `Move an agent to another leo host (a client.hosts entry): its workspace,
conversation and git state follow it, it resumes there, and this host keeps a
dormant record pointing at the new home.

The destination must have the agent's template and environments and the same
home path. A busy agent is refused unless --force, which cuts its turn off.
A failure before the destination starts leaves the agent stopped here and not
marked moved; ` + "`leo agent start <name>`" + ` recovers it, and re-running the move is
safe. The move runs in the daemon, so it finishes even if this command is
interrupted.

If the destination's answer to the final start is lost and its status does
not settle it, the agent stays fenced here (move_pending): nothing starts or
moves it until --reconcile asks the destination how that move ended. The
daemon also reconciles fenced agents when it starts. --abandon-remote lifts
the fence without asking, for a destination that is gone for good: if the
agent is live there, starting it here leaves two live copies. See
docs/configuration/agent-move.md.`,
		Example: `  leo agent move web --to laptop
  leo agent move web --reconcile
  leo agent move web --abandon-remote`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_agent_move"); err != nil {
				return err
			}
			if reconcile || abandon {
				if to != "" || force || (reconcile && abandon) {
					return errors.New("--reconcile and --abandon-remote settle the pending move on its own; they take no --to, --force or each other")
				}
				if reconcile {
					return runMoveSettle(cmd.Context(), host, args[0], []string{"--reconcile"}, asJSON, moveReconcileFn)
				}
				if !assumeYes {
					ok, err := confirmAbandon(args[0])
					if err != nil || !ok {
						return err
					}
				}
				return runMoveSettle(cmd.Context(), host, args[0], []string{"--abandon-remote", "--yes"}, asJSON, moveAbandonFn)
			}
			if to == "" {
				return errors.New("name the destination with --to <host> (a client.hosts entry)")
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, forwardFlags([]string{"move", args[0], "--to", to}, map[string]bool{"--force": force, "--json": asJSON}))
			}
			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, args[0])
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			req := agentmove.Request{Mode: agentmove.ModeMove, To: to, Force: force}
			return runMoveJob(cmd.Context(), cfg.HomePath, resolved.Name, req, asJSON)
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().StringVar(&to, "to", "", "destination host (a client.hosts entry)")
	cmd.Flags().BoolVar(&force, "force", false, "move a busy agent anyway, cutting its turn off")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the finished job as JSON instead of progress")
	cmd.Flags().BoolVar(&reconcile, "reconcile", false, "settle a pending move (move_pending) by asking its destination how it ended")
	cmd.Flags().BoolVar(&abandon, "abandon-remote", false, "lift a pending move's fence without asking its destination (risks two live copies)")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the --abandon-remote confirmation prompt")
	return cmd
}

// abandonWarning is what --abandon-remote risks.
const abandonWarning = "if the agent is live on the destination, starting it here leaves two live copies of one conversation"

// confirmAbandon asks before --abandon-remote lifts query's fence; without a
// TTY it refuses and points at --yes.
func confirmAbandon(query string) (bool, error) {
	if !agentIsTTY() {
		return false, fmt.Errorf("refusing to abandon %s's pending move without confirmation (%s): pass --yes to skip the prompt when stdin is not a TTY", query, abandonWarning)
	}
	fmt.Fprintf(agentStdout, "warning: %s\n", abandonWarning)
	if !prompt.YesNo(bufio.NewReader(agentStdin), fmt.Sprintf("Abandon %s's pending move without asking its destination?", query), false) {
		fmt.Fprintln(agentStdout, "Aborted.")
		return false, nil
	}
	return true, nil
}

// settleFn settles a pending move through the daemon at home.
type settleFn func(ctx context.Context, home, name string) (agentmove.SettleResult, error)

// runMoveSettle settles query's pending move with settle, here or on host
// (forwarding flags).
func runMoveSettle(ctx context.Context, host, query string, flags []string, asJSON bool, settle settleFn) error {
	cfg, res, err := dispatch(host)
	if err != nil {
		return err
	}
	if !res.Localhost {
		return runRemote(res, forwardFlags(append([]string{"move", query}, flags...), map[string]bool{"--json": asJSON}))
	}
	resolved, err := daemon.AgentResolve(ctx, cfg.HomePath, query)
	if err != nil {
		return fmt.Errorf("resolving agent: %w", err)
	}
	r, err := settle(ctx, cfg.HomePath, resolved.Name)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(agentStdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	printSettled(r)
	return nil
}

func printSettled(r agentmove.SettleResult) {
	switch r.Outcome {
	case agentmove.SettleMoved:
		fmt.Fprintf(agentStdout, "%s is on %s: marked moved here\nattach with: leo --host %s agent attach %s\n", r.Name, r.To, r.To, r.Name)
	case agentmove.SettleFailed:
		fmt.Fprintf(agentStdout, "%s's move failed on %s: the fence is lifted; `leo agent start %s` recovers it here\n", r.Name, r.To, r.Name)
	case agentmove.SettleAbandoned:
		fmt.Fprintf(agentStdout, "%s's fence is lifted without asking %s; %s\n", r.Name, r.To, abandonWarning)
	default:
		fmt.Fprintf(agentStdout, "%s's move to %s: %s\n", r.Name, r.To, r.Outcome)
	}
}

func newAgentCopyCmd() *cobra.Command {
	var host, to, name, branch string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "copy <name>",
		Short: "Fork an agent into an independent copy, here or on another host",
		Long: `Copy an agent: a new agent starts from the same conversation and
workspace state, and from then on the two diverge. The source keeps running.

The copy is named <name>-copy (or -copy-2, ...) unless --name, and works in
<workspace>-<copy name>. In git it gets its own branch, <branch>-<copy name>
unless --branch (needed when HEAD is detached). Without --to the copy lands on
this host. A claude agent can be copied mid-turn (the copy ends at the last
completed turn); codex and opencode agents only between turns. A copy of an
agent backing a persistent task is not wired to the task.`,
		Example:           "  leo agent copy web\n  leo agent copy web --to laptop --name web-laptop",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_agent_copy"); err != nil {
				return err
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				if err := gateRemoteCopy(cmd.CommandPath(), res, args[0]); err != nil {
					return err
				}
				fwd := []string{"copy", args[0]}
				for _, f := range [][2]string{{"--to", to}, {"--name", name}, {"--branch", branch}} {
					if f[1] != "" {
						fwd = append(fwd, f[0], f[1])
					}
				}
				return runRemote(res, forwardFlags(fwd, map[string]bool{"--json": asJSON}))
			}
			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, args[0])
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			if err := gateSpawnTemplate(cmd, resolved.Template); err != nil {
				return err
			}
			req := agentmove.Request{Mode: agentmove.ModeCopy, To: to, Name: name, Branch: branch}
			return runMoveJob(cmd.Context(), cfg.HomePath, resolved.Name, req, asJSON)
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().StringVar(&to, "to", "", "destination host (a client.hosts entry); default this host")
	cmd.Flags().StringVar(&name, "name", "", "the copy's name (default <name>-copy)")
	cmd.Flags().StringVar(&branch, "branch", "", "the copy's git branch (default <branch>-<copy name>)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the finished job as JSON instead of progress")
	return cmd
}

// gateRemoteCopy applies the spawn gate to a copy forwarded to another host:
// ssh does not carry LEO_PERMISSIONS, so the remote end cannot. A can_spawn
// list needs the agent's template, which only the remote daemon knows.
func gateRemoteCopy(label string, res config.HostResolution, name string) error {
	if err := gateToolFor(label, "leo_spawn_agent"); err != nil {
		return err
	}
	perms, ok := permissionsFromEnv()
	if !ok {
		return malformedPermissionsError(label)
	}
	if len(perms.CanSpawn) == 0 {
		return nil
	}
	resolved, err := resolveRemoteAgent(res, name)
	if err != nil {
		return err
	}
	return gateSpawnTemplateFor(label, resolved.Template)
}

// forwardFlags appends each set boolean flag, in a fixed order.
func forwardFlags(args []string, flags map[string]bool) []string {
	for _, f := range []string{"--force", "--json"} {
		if flags[f] {
			args = append(args, f)
		}
	}
	return args
}

// runMoveJob starts the job and follows it to the end, printing each stage
// and note as it appears (or, with asJSON, only the finished job).
func runMoveJob(ctx context.Context, home, agentName string, req agentmove.Request, asJSON bool) error {
	st, err := moveStartFn(ctx, home, agentName, req)
	if err != nil {
		return err
	}
	var seen progressSeen
	for {
		if !asJSON {
			seen.print(st)
		}
		if st.State != agentmove.JobRunning {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stopped following job %s, which carries on in the daemon: %w", st.ID, ctx.Err())
		case <-movePollTick():
		}
		id := st.ID
		if st, err = moveJobFn(ctx, home, id); err != nil {
			return fmt.Errorf("polling job %s: %w", id, err)
		}
	}
	if asJSON {
		enc := json.NewEncoder(agentStdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(st); err != nil {
			return err
		}
	}
	if st.State == agentmove.JobFailed {
		return jobError(st)
	}
	if !asJSON {
		printJobDone(st)
	}
	return nil
}

// progressSeen is how much of a job has been printed.
type progressSeen struct{ stages, notes int }

func (p *progressSeen) print(st agentmove.JobStatus) {
	for ; p.stages < len(st.Stages); p.stages++ {
		fmt.Fprintf(agentStdout, "  · %s\n", st.Stages[p.stages].Name)
	}
	for ; p.notes < len(st.Notes); p.notes++ {
		fmt.Fprintf(agentStdout, "  note: %s\n", st.Notes[p.notes].Message)
	}
}

func jobError(st agentmove.JobStatus) error {
	if len(st.Problems) > 0 {
		lines := make([]string, len(st.Problems))
		for i, p := range st.Problems {
			lines[i] = fmt.Sprintf("  - %s: %s", p.Code, p.Message)
		}
		return fmt.Errorf("%s refused:\n%s", st.Request.Mode, strings.Join(lines, "\n"))
	}
	if st.Error != nil {
		return errors.New(st.Error.Message)
	}
	return fmt.Errorf("job %s failed", st.ID)
}

func printJobDone(st agentmove.JobStatus) {
	r := st.Result
	if r == nil {
		return
	}
	hostFlag := ""
	if r.To != "" {
		hostFlag = "--host " + r.To + " "
	}
	if st.Request.Mode == agentmove.ModeCopy {
		where := "here"
		if r.To != "" {
			where = "on " + r.To
		}
		fmt.Fprintf(agentStdout, "copied to %s %s (workspace %s", r.Name, where, r.Workspace)
		if r.Branch != "" {
			fmt.Fprintf(agentStdout, ", branch %s", r.Branch)
		}
		fmt.Fprintln(agentStdout, ")")
	} else {
		fmt.Fprintf(agentStdout, "moved %s to %s (%s)\n", r.Name, r.To, r.Host)
	}
	fmt.Fprintf(agentStdout, "attach with: leo %sagent attach %s\n", hostFlag, r.Name)
}
