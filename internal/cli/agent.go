package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// agentSessionName is the supervisor's stable session-name convention.
func agentSessionName(name string) string { return agent.SessionName(name) }

// processSessionName matches internal/service.ProcessSpec.Name — the supervisor
// creates `leo-<name>` tmux sessions for configured processes.
func processSessionName(name string) string { return "leo-" + name }

// Testability seams — overridden in tests.
var (
	agentExecCommand           = exec.Command
	agentSyscallExec           = syscall.Exec
	agentStderr      io.Writer = os.Stderr
	agentStdout      io.Writer = os.Stdout
	agentStdin       io.Reader = os.Stdin
	agentIsTTY                 = defaultIsTTY
)

// defaultIsTTY returns true when stdin is a character device (i.e. the user is
// typing interactively). Used to decide whether to block on a collision prompt.
func defaultIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Spawn and control ephemeral Claude agents on a leo server",
		Long: `Manage ephemeral agents running under a leo server supervisor.

When --host is omitted, the command talks to the local daemon socket.
When a host is configured in leo.yaml (client.hosts) the CLI shells out to
  ssh <host> leo agent <subcommand>
so remote calls use your existing SSH setup.`,
	}

	cmd.AddCommand(
		newAgentListCmd(),
		newAgentSpawnCmd(),
		newAgentAttachCmd(),
		newAgentStopCmd(),
		newAgentPruneCmd(),
		newAgentLogsCmd(),
		newAgentSessionNameCmd(),
	)
	return cmd
}

// hostFlag is the --host value, shared across subcommands.
func addHostFlag(cmd *cobra.Command, host *string) {
	cmd.Flags().StringVar(host, "host", "", `remote host name (from client.hosts), or "localhost"`)
}

// addControlModeFlag wires a --cc flag that enables tmux control mode on
// attach. Terminals like iTerm2 and WezTerm render tmux -CC sessions as
// native tabs. Control mode is local-only — attach helpers reject it when
// combined with SSH dispatch or when already inside a tmux client.
func addControlModeFlag(cmd *cobra.Command, cc *bool) {
	cmd.Flags().BoolVar(cc, "cc", false, "use tmux control mode (-CC) — for iTerm2/WezTerm native tabs; local only")
}

// dispatch handles the "run this locally vs proxy via ssh" decision. For
// subcommands that need special handling (attach) callers read hostRes directly.
func dispatch(flagHost string) (*config.Config, config.HostResolution, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, config.HostResolution{}, err
	}
	res, err := cfg.ResolveHost(flagHost)
	if err != nil {
		return nil, config.HostResolution{}, err
	}
	return cfg, res, nil
}

// buildSSHArgs returns a fresh slice of SSH argv so appends at the call site
// cannot alias the config-loaded SSHArgs backing array.
func buildSSHArgs(res config.HostResolution, tail ...string) []string {
	args := make([]string, 0, 1+len(res.Host.SSHArgs)+len(tail))
	args = append(args, res.Host.SSH)
	args = append(args, res.Host.SSHArgs...)
	args = append(args, tail...)
	return args
}

// runRemote executes `ssh <host> <leo_path> agent <subcmd args...>` forwarding
// stdio. The remote binary path comes from HostConfig.LeoPath or defaults to
// config.DefaultRemoteLeoPath — SSH's non-interactive shell typically doesn't
// source .zshrc, so relying on bare "leo" in PATH is fragile.
func runRemote(res config.HostResolution, subcmdArgs []string) error {
	tail := append([]string{res.Host.RemoteLeoPath(), "agent"}, subcmdArgs...)
	args := buildSSHArgs(res, tail...)
	cmd := agentExecCommand("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = agentStdout
	cmd.Stderr = agentStderr
	return cmd.Run()
}

// --- list ---

func newAgentListCmd() *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List running agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"list"}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemote(res, extra)
			}

			records, err := daemon.AgentList(cfg.HomePath)
			if err != nil {
				return fmt.Errorf("listing agents: %w", err)
			}

			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(records)
			}
			if len(records) == 0 {
				fmt.Fprintln(agentStdout, "No agents running.")
				return nil
			}
			tw := tabwriter.NewWriter(agentStdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTEMPLATE\tBRANCH\tWORKSPACE\tSTATUS\tRESTARTS")
			for _, r := range records {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
					r.Name, dashIfEmpty(r.Template), dashIfEmpty(r.Branch),
					dashIfEmpty(r.Workspace), dashIfEmpty(r.Status), r.Restarts)
			}
			return tw.Flush()
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// --- spawn ---

func newAgentSpawnCmd() *cobra.Command {
	var host, repo, name, branch, base string
	var reuseOwner, attachExisting bool
	cmd := &cobra.Command{
		Use:   "spawn <template> [repo]",
		Short: "Spawn a new agent from a template",
		Long: `Spawn a new ephemeral agent from a template. Repo can be passed as a
positional arg or via --repo. Use owner/repo to clone a canonical repo, or a
plain name to reuse the template workspace.

When repo is slashless and matches an existing agent's short name, the CLI
prompts the user for how to proceed: attach to the existing agent, spawn using
that agent's canonical owner/repo, or spawn a fresh template workspace. When
repo is slashed (owner/repo) and an agent already targets the same repo and
branch, the CLI prompts to attach or spawn a fresh suffixed agent. The prompt
is skipped in non-interactive runs (no TTY) — in that case the command errors
unless --attach-existing or --reuse-owner is set. Flags override the prompt:
--reuse-owner forces the canonical repo, --attach-existing attaches instead.`,
		Example: `  # Spawn an agent from the 'mcp-node' template using the template workspace
  leo agent spawn mcp-node

  # Spawn against a specific repo with a dedicated git worktree
  leo agent spawn mcp-node owner/fetch --worktree feat/new-endpoint

  # Non-interactive: attach to the existing agent on collision
  leo agent spawn mcp-node leo --attach-existing`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			template := args[0]
			if len(args) == 2 {
				if repo != "" {
					return fmt.Errorf("repo given both as positional arg and --repo flag; pick one")
				}
				repo = args[1]
			}
			if repo == "" {
				return fmt.Errorf("repo is required (pass as positional or --repo)")
			}
			if reuseOwner && attachExisting {
				return fmt.Errorf("--reuse-owner and --attach-existing are mutually exclusive")
			}
			if branch != "" && !strings.Contains(repo, "/") {
				return fmt.Errorf("--worktree requires owner/repo; got %q", repo)
			}
			if base != "" && branch == "" {
				return fmt.Errorf("--base only applies with --worktree")
			}

			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"spawn", template, "--repo", repo}
				if name != "" {
					extra = append(extra, "--name", name)
				}
				if reuseOwner {
					extra = append(extra, "--reuse-owner")
				}
				if attachExisting {
					extra = append(extra, "--attach-existing")
				}
				if branch != "" {
					extra = append(extra, "--worktree", branch)
				}
				if base != "" {
					extra = append(extra, "--base", base)
				}
				return runRemote(res, extra)
			}

			// Collision detection: slashless repos match by repo short-name
			// (ambiguous owner), slashed repos match exactly on (Repo, Branch).
			if !strings.Contains(repo, "/") {
				matches, err := findRepoShortMatches(cfg.HomePath, repo)
				if err != nil {
					return fmt.Errorf("checking existing agents: %w", err)
				}
				switch {
				case len(matches) == 0:
					// No conflict — fall through and spawn.
				case len(matches) == 1:
					choice, err := resolveSpawnCollision(matches[0], template, reuseOwner, attachExisting)
					if err != nil {
						return err
					}
					switch choice {
					case spawnAttachExisting:
						return attachLocal(cfg.HomePath, matches[0].Name, attachOptions{})
					case spawnUseCanonicalRepo:
						repo = matches[0].Repo
					case spawnFreshTemplate:
						// fall through unchanged
					}
				default:
					labels := make([]string, 0, len(matches))
					for _, m := range matches {
						labels = append(labels, fmt.Sprintf("%s (%s)", m.Name, m.Repo))
					}
					return fmt.Errorf("multiple existing agents match %q: %s — pass the full owner/repo or run 'leo agent list' to disambiguate",
						repo, strings.Join(labels, ", "))
				}
			} else {
				matches, err := findExactMatches(cfg.HomePath, repo, branch)
				if err != nil {
					return fmt.Errorf("checking existing agents: %w", err)
				}
				switch {
				case len(matches) == 0:
					// No conflict — fall through and spawn.
				case len(matches) == 1:
					choice, err := resolveExactCollision(matches[0], template, attachExisting)
					if err != nil {
						return err
					}
					switch choice {
					case spawnAttachExisting:
						return attachLocal(cfg.HomePath, matches[0].Name, attachOptions{})
					case spawnFreshTemplate:
						// fall through — reserveUniqueName suffixes the name.
					}
				default:
					labels := make([]string, 0, len(matches))
					for _, m := range matches {
						labels = append(labels, m.Name)
					}
					return fmt.Errorf("multiple existing agents target %s (branch %q): %s — stop one before spawning another",
						repo, branch, strings.Join(labels, ", "))
				}
			}

			rec, err := daemon.AgentSpawn(cfg.HomePath, daemon.AgentSpawnRequest{
				Template: template,
				Repo:     repo,
				Name:     name,
				Branch:   branch,
				Base:     base,
			})
			if err != nil {
				return fmt.Errorf("spawning agent: %w", err)
			}
			if rec.Branch != "" {
				fmt.Fprintf(agentStdout, "spawned %s (branch: %s, worktree: %s)\n", rec.Name, rec.Branch, rec.Workspace)
			} else {
				fmt.Fprintf(agentStdout, "spawned %s (workspace: %s)\n", rec.Name, rec.Workspace)
			}
			fmt.Fprintf(agentStdout, "attach with: leo agent attach %s\n", rec.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo to clone, or plain name for template workspace")
	cmd.Flags().StringVar(&name, "name", "", "override the derived agent name")
	cmd.Flags().StringVar(&branch, "worktree", "", "create a dedicated git worktree on this branch (requires owner/repo)")
	cmd.Flags().StringVar(&base, "base", "", "base ref for new branches (defaults to origin HEAD)")
	cmd.Flags().BoolVar(&reuseOwner, "reuse-owner", false, "on collision, spawn using the existing agent's canonical owner/repo")
	cmd.Flags().BoolVar(&attachExisting, "attach-existing", false, "on collision, attach to the existing agent instead of spawning")
	return cmd
}

// spawnChoice is the result of the collision prompt.
type spawnChoice int

const (
	spawnFreshTemplate spawnChoice = iota
	spawnAttachExisting
	spawnUseCanonicalRepo
	spawnCancel
)

// findRepoShortMatches queries the daemon and returns records whose Repo has
// a short segment matching query (case-insensitive). Slashless stored Repos
// match by their full value. Records with no Repo are skipped.
//
// Scope: this consults the daemon's live agent list only — stopped agents are
// not considered. By design, once an agent is stopped its repo short-name is
// immediately free for reuse; the collision prompt exists to prevent two
// running agents from silently sharing a short-name, not to reserve names
// across the agent's full history.
func findRepoShortMatches(homePath, query string) ([]agent.Record, error) {
	records, err := daemon.AgentList(homePath)
	if err != nil {
		return nil, err
	}
	var out []agent.Record
	for _, r := range records {
		short := agent.ShortRepo(r.Repo)
		if short == "" {
			continue
		}
		if strings.EqualFold(short, query) {
			out = append(out, r)
		}
	}
	return out, nil
}

// findExactMatches returns running agents whose Repo (case-insensitive) and
// Branch (exact, including both-empty) match the target spawn spec. This is the
// slashed-repo analogue of findRepoShortMatches: a hit means the caller is
// asking to re-spawn the same workspace, not merely one that shares a short
// name.
func findExactMatches(homePath, repo, branch string) ([]agent.Record, error) {
	records, err := daemon.AgentList(homePath)
	if err != nil {
		return nil, err
	}
	return filterExactMatches(records, repo, branch), nil
}

// filterExactMatches is the pure part of findExactMatches, split out for tests.
func filterExactMatches(records []agent.Record, repo, branch string) []agent.Record {
	var out []agent.Record
	for _, r := range records {
		if strings.EqualFold(r.Repo, repo) && r.Branch == branch {
			out = append(out, r)
		}
	}
	return out
}

// resolveSpawnCollision decides what to do when a slashless repo query matches
// exactly one existing agent. Flags force a non-interactive choice; otherwise
// the user is prompted when a TTY is attached. Non-interactive CLI runs with
// no flags return a typed error so scripts fail loudly instead of silently
// spawning a duplicate template workspace.
// (The web UI does not reach this path — it calls the daemon directly.)
func resolveSpawnCollision(match agent.Record, template string, reuseOwner, attachExisting bool) (spawnChoice, error) {
	switch {
	case attachExisting:
		return spawnAttachExisting, nil
	case reuseOwner:
		if match.Repo == "" {
			return spawnCancel, fmt.Errorf("--reuse-owner set but existing agent %s has no stored repo", match.Name)
		}
		return spawnUseCanonicalRepo, nil
	case !agentIsTTY():
		target := match.Repo
		if target == "" {
			target = match.Name
		}
		return spawnCancel, fmt.Errorf(
			"agent %s already targets %s and stdin is not a TTY; pass --attach-existing to attach, --reuse-owner to spawn using the existing canonical owner/repo, or pass the full owner/repo to disambiguate",
			match.Name, target)
	}

	if match.Repo != "" {
		fmt.Fprintf(agentStderr, "\nAn agent already targets %s:\n", match.Repo)
	} else {
		fmt.Fprintf(agentStderr, "\nAn agent already matches %q:\n", match.Name)
	}
	fmt.Fprintf(agentStderr, "  name:     %s\n", match.Name)
	fmt.Fprintf(agentStderr, "  template: %s\n\n", dashIfEmpty(match.Template))
	fmt.Fprintln(agentStderr, "  a) attach to the existing agent")
	if match.Repo != "" {
		fmt.Fprintf(agentStderr, "  b) spawn a new agent using that canonical repo (%s)\n", match.Repo)
	}
	fmt.Fprintf(agentStderr, "  c) spawn a fresh agent under template %q (current behavior)\n", template)
	fmt.Fprintln(agentStderr, "  q) cancel")
	if _, err := fmt.Fprint(agentStderr, "\nchoice [c]: "); err != nil {
		return spawnCancel, fmt.Errorf("writing prompt: %w", err)
	}

	reader := bufio.NewReader(agentStdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return spawnCancel, fmt.Errorf("reading choice: %w", err)
	}
	choice := strings.ToLower(strings.TrimSpace(line))
	// EOF with an empty line (piped input that closed, Ctrl-D without input)
	// is treated as cancel — silently defaulting to "fresh template" would
	// surprise a user who closed stdin expecting the command to abort.
	if errors.Is(err, io.EOF) && choice == "" {
		return spawnCancel, fmt.Errorf("spawn cancelled (stdin closed)")
	}
	switch choice {
	case "a":
		return spawnAttachExisting, nil
	case "b":
		if match.Repo == "" {
			return spawnCancel, fmt.Errorf("existing agent %s has no stored repo; cannot reuse owner", match.Name)
		}
		return spawnUseCanonicalRepo, nil
	case "", "c":
		return spawnFreshTemplate, nil
	case "q":
		return spawnCancel, fmt.Errorf("spawn cancelled")
	default:
		return spawnCancel, fmt.Errorf("unknown choice %q", choice)
	}
}

// resolveExactCollision handles (Repo, Branch) collisions for slashed
// owner/repo spawns. The "reuse canonical repo" option is not offered here
// because the user already supplied the canonical repo — the only meaningful
// choices are attach, spawn-fresh (with numeric suffix), or cancel.
// Non-interactive callers must opt in explicitly via --attach-existing;
// otherwise the command errors rather than silently suffixing a duplicate.
func resolveExactCollision(match agent.Record, template string, attachExisting bool) (spawnChoice, error) {
	switch {
	case attachExisting:
		return spawnAttachExisting, nil
	case !agentIsTTY():
		target := match.Repo
		if match.Branch != "" {
			target = fmt.Sprintf("%s on branch %s", match.Repo, match.Branch)
		}
		return spawnCancel, fmt.Errorf(
			"agent %s already targets %s and stdin is not a TTY; pass --attach-existing to attach or stop the existing agent first",
			match.Name, target)
	}

	fmt.Fprintf(agentStderr, "\nAn agent already targets %s", match.Repo)
	if match.Branch != "" {
		fmt.Fprintf(agentStderr, " on branch %s", match.Branch)
	}
	fmt.Fprintln(agentStderr, ":")
	fmt.Fprintf(agentStderr, "  name:     %s\n", match.Name)
	fmt.Fprintf(agentStderr, "  template: %s\n\n", dashIfEmpty(match.Template))
	fmt.Fprintln(agentStderr, "  a) attach to the existing agent")
	fmt.Fprintf(agentStderr, "  c) spawn a fresh agent under template %q (current behavior)\n", template)
	fmt.Fprintln(agentStderr, "  q) cancel")
	if _, err := fmt.Fprint(agentStderr, "\nchoice [c]: "); err != nil {
		return spawnCancel, fmt.Errorf("writing prompt: %w", err)
	}

	reader := bufio.NewReader(agentStdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return spawnCancel, fmt.Errorf("reading prompt: %w", err)
	}
	choice := strings.TrimSpace(strings.ToLower(line))
	if errors.Is(err, io.EOF) && choice == "" {
		return spawnCancel, fmt.Errorf("spawn cancelled (stdin closed)")
	}
	switch choice {
	case "a":
		return spawnAttachExisting, nil
	case "", "c":
		return spawnFreshTemplate, nil
	case "q":
		return spawnCancel, fmt.Errorf("spawn cancelled")
	default:
		return spawnCancel, fmt.Errorf("unknown choice %q", choice)
	}
}

// attachLocal performs the local tmux-attach flow: look up the canonical
// session via the daemon, then delegate to attachTmuxSession so every flavor
// of attach (socket selector, nested-tmux popup, --cc) stays in one place.
// Shared between `leo agent attach` and the spawn collision prompt's
// "attach-existing" branch.
func attachLocal(homePath, query string, opts attachOptions) error {
	session, err := daemon.AgentSession(homePath, query)
	if err != nil {
		return fmt.Errorf("looking up session: %w", err)
	}
	return attachTmuxSession(config.HostResolution{Localhost: true}, session, opts)
}

// --- attach ---

func newAgentAttachCmd() *cobra.Command {
	var host string
	var cc bool
	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to the agent's tmux session",
		Long: `Attach to a running agent's tmux session. Locally this replaces the
current process with tmux so the TUI has full control of the terminal.
Remotely it runs 'ssh -t <host> tmux -L leo attach -t leo-<name>'. Detach
with the usual tmux prefix + d (default: C-b d).

When you're already inside a tmux client, Leo opens a display-popup overlay
that runs the attach — dismissing the popup returns you to your outer tmux.
Pass --cc in a tmux-aware terminal (iTerm2, WezTerm) to render the session
as a native tab via tmux control mode.`,
		Example: `  # Attach to an agent by canonical name
  leo agent attach leo-mcp-node-owner-fetch

  # Or by a unique shorthand the daemon can resolve
  leo agent attach fetch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}

			if !res.Localhost {
				// Remote: resolve the shorthand through the remote daemon first,
				// then SSH -t attach to the canonical tmux session. Going via the
				// daemon lets the user pass shorthand (repo, short suffix) over SSH
				// and surfaces clear "no match" / "ambiguous" errors before attach.
				session, err := resolveRemoteSession(res, name)
				if err != nil {
					return err
				}
				return attachTmuxSession(res, session, attachOptions{cc: cc})
			}

			return attachLocal(cfg.HomePath, name, attachOptions{cc: cc})
		},
	}
	addHostFlag(cmd, &host)
	addControlModeFlag(cmd, &cc)
	return cmd
}

// resolveRemoteSession shells `ssh <host> leo agent session-name <query>` to
// ask the remote daemon for the canonical tmux session. Going through the
// daemon lets the user pass shorthand over SSH and surface clear "no match" /
// "ambiguous" errors before the tmux attach.
func resolveRemoteSession(res config.HostResolution, query string) (string, error) {
	args := buildSSHArgs(res, res.Host.RemoteLeoPath(), "agent", "session-name", query)
	cmd := agentExecCommand("ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("resolving remote agent %q: %w: %s", query, err, msg)
		}
		return "", fmt.Errorf("resolving remote agent %q: %w", query, err)
	}
	session := strings.TrimSpace(string(out))
	if session == "" {
		return "", fmt.Errorf("remote returned empty session name for %q", query)
	}
	return session, nil
}

// --- session-name ---

func newAgentSessionNameCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "session-name <query>",
		Short: "Print the tmux session name for an agent (supports shorthand)",
		Long: `Resolve a shorthand query to the canonical tmux session name and print it
to stdout. Useful as a building block for shell scripts and the remote attach
flow. The query can be an agent name, the canonical repo, a repo short name,
or any unambiguous suffix.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"session-name", query})
			}
			resolved, err := daemon.AgentResolve(cfg.HomePath, query)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			fmt.Fprintln(agentStdout, resolved.Session)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// --- stop ---

func newAgentStopCmd() *cobra.Command {
	var host string
	var prune, force, deleteBranch bool
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running agent",
		Long: `Stop a running agent's tmux session. Worktree agents preserve their
on-disk worktree so you can reattach or inspect state; pass --prune to also
remove the worktree and agentstore record in one step.`,
		Example: `  # Stop an agent but keep its worktree on disk
  leo agent stop leo-mcp-node-owner-fetch

  # Stop and clean up worktree + local branch
  leo agent stop leo-mcp-node-owner-fetch --prune --delete-branch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if (force || deleteBranch) && !prune {
				return fmt.Errorf("--force and --delete-branch require --prune")
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"stop", name}
				if prune {
					extra = append(extra, "--prune")
				}
				if force {
					extra = append(extra, "--force")
				}
				if deleteBranch {
					extra = append(extra, "--delete-branch")
				}
				return runRemote(res, extra)
			}

			// Resolve shorthand locally first so the prune step can use the
			// canonical name (Prune does not go through Resolve because the
			// agent is stopped by then and the resolver only matches live
			// agents).
			resolved, err := daemon.AgentResolve(cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			canonical := resolved.Name

			if err := daemon.AgentStop(cfg.HomePath, canonical); err != nil {
				return fmt.Errorf("stopping agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "stopped %s\n", canonical)

			if prune {
				if err := daemon.AgentPrune(cfg.HomePath, canonical, daemon.AgentPruneRequest{
					Force:        force,
					DeleteBranch: deleteBranch,
				}); err != nil {
					if errors.Is(err, agent.ErrNotWorktreeAgent) {
						// Stop already cleared a shared-workspace record;
						// nothing to prune. Treat as a no-op rather than an
						// error so --prune is safe to default-on in scripts.
						return nil
					}
					return fmt.Errorf("pruning worktree: %w", err)
				}
				fmt.Fprintf(agentStdout, "pruned worktree for %s\n", canonical)
			}
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&prune, "prune", false, "also remove the worktree and agentstore record (worktree agents only)")
	cmd.Flags().BoolVar(&force, "force", false, "with --prune: remove even when the worktree is dirty")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "with --prune: delete the local branch after removing the worktree")
	return cmd
}

// --- prune ---

func newAgentPruneCmd() *cobra.Command {
	var host string
	var force, deleteBranch bool
	cmd := &cobra.Command{
		Use:   "prune <name>",
		Short: "Remove a stopped worktree agent's worktree and record",
		Long: `Remove the on-disk worktree and agentstore record for a worktree agent
that has already been stopped. No-op for shared-workspace agents. Pass
--force to override the dirty-worktree check, or --delete-branch to also
delete the local branch after the worktree is gone.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"prune", name}
				if force {
					extra = append(extra, "--force")
				}
				if deleteBranch {
					extra = append(extra, "--delete-branch")
				}
				return runRemote(res, extra)
			}
			if err := daemon.AgentPrune(cfg.HomePath, name, daemon.AgentPruneRequest{
				Force:        force,
				DeleteBranch: deleteBranch,
			}); err != nil {
				return fmt.Errorf("pruning agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "pruned %s\n", name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&force, "force", false, "remove even when the worktree is dirty or the branch is unmerged")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "delete the local branch after the worktree is removed")
	return cmd
}

// --- logs ---

func newAgentLogsCmd() *cobra.Command {
	var host string
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show recent output from an agent's tmux pane",
		Example: `  # Show the last 200 lines (default)
  leo agent logs leo-mcp-node-owner-fetch

  # Tail a specific count, then follow live output
  leo agent logs leo-mcp-node-owner-fetch -n 500 --follow`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}

			if follow {
				// Follow mode is a `tail -f` on the tmux pane — simpler than
				// streaming over the socket. Remote follow uses ssh.
				return followTmuxSession(res, agentSessionName(name), lines)
			}

			if !res.Localhost {
				extra := []string{"logs", name, "-n", fmt.Sprintf("%d", lines)}
				return runRemote(res, extra)
			}

			output, err := daemon.AgentLogs(cfg.HomePath, name, lines)
			if err != nil {
				return fmt.Errorf("fetching logs: %w", err)
			}
			if _, err := fmt.Fprint(agentStdout, output); err != nil {
				return fmt.Errorf("writing logs: %w", err)
			}
			if !strings.HasSuffix(output, "\n") {
				fmt.Fprintln(agentStdout)
			}
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "number of trailing lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream output (tail -f)")
	return cmd
}

// completeAgentNames supplies shell-completion values for commands that take an
// agent name. It queries the local daemon's live agent list — the same source
// `leo agent list` shows — and returns agent names. Daemon unreachable or any
// other failure returns ShellCompDirectiveNoFileComp with no values so the
// shell falls back to no completion rather than suggesting filenames.
func completeAgentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	records, err := daemon.AgentList(cfg.HomePath)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(records))
	for _, r := range records {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// --- helpers ---

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ensure package agent stays referenced even though current code only uses
// daemon.Agent* helpers and agent.Record.
var _ = agent.Record{}
