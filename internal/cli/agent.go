package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

// agentSessionName is the supervisor's stable session-name convention.
func agentSessionName(name string) string { return agent.SessionName(name) }

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
		newAgentWorktreeCmd(),
		newAgentAttachCmd(),
		newAgentStopCmd(),
		newAgentStartCmd(),
		newAgentDeleteCmd(),
		newAgentDeletePlanCmd(),
		newAgentResetCmd(),
		newAgentRestartCmd(),
		newAgentSetTemplateCmd(),
		newAgentRenameCmd(),
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
// attach. Terminals like iTerm2 and WezTerm render tmux -CC sessions as native
// tabs. Works locally and over SSH (remote -CC streams through ssh -tt; see
// attachRemoteControlMode); the only place it's rejected is from inside an
// existing tmux client, where -CC would fight the outer server for the terminal.
func addControlModeFlag(cmd *cobra.Command, cc *bool) {
	cmd.Flags().BoolVar(cc, "cc", false, "use tmux control mode (-CC) for iTerm2/WezTerm native tabs (local or over SSH)")
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
	control := sshControlOpts(res)
	args := make([]string, 0, 1+len(res.Host.SSHArgs)+len(control)+len(tail))
	args = append(args, res.Host.SSH)
	args = append(args, res.Host.SSHArgs...)
	args = append(args, control...)
	args = append(args, tail...)
	return args
}

// runRemote executes `ssh <host> <leo_path> agent <subcmd args...>`.
func runRemote(res config.HostResolution, subcmdArgs []string) error {
	return runRemoteGroup(res, "agent", subcmdArgs)
}

// runRemoteGroup executes `ssh <host> <leo_path> <group> <subcmd args...>`
// forwarding stdio. The remote binary path comes from HostConfig.LeoPath or
// defaults to config.DefaultRemoteLeoPath — SSH's non-interactive shell
// typically doesn't source .zshrc, so relying on bare "leo" in PATH is
// fragile.
func runRemoteGroup(res config.HostResolution, group string, subcmdArgs []string) error {
	tail := append([]string{res.Host.RemoteLeoPath(), group}, subcmdArgs...)
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

			records, err := agentListFn(cmd.Context(), cfg.HomePath)
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
	var host, repo, name, branch, base, prompt, idleSuspend string
	var envPairs []string
	var reuseOwner, attachExisting, asJSON bool
	cmd := &cobra.Command{
		Use:   "spawn <template> [repo]",
		Short: "Spawn a new agent from a template",
		Long: `Spawn a new ephemeral agent from a template. Repo is optional and can be
passed as a positional arg or via --repo. Use owner/repo to clone a canonical
repo, a plain name to reuse the template workspace under a per-name subdir, or
omit it entirely to run the template as-is directly in its own workspace (the
agent is named after the template).

When repo is slashless and matches an existing agent's short name, the CLI
prompts the user for how to proceed: attach to the existing agent, spawn using
that agent's canonical owner/repo, or spawn a fresh template workspace. When
repo is slashed (owner/repo) and an agent already targets the same repo and
branch, the CLI prompts to attach or spawn a fresh suffixed agent. The prompt
is skipped in non-interactive runs (no TTY) — in that case the command errors
unless --attach-existing or --reuse-owner is set. Flags override the prompt:
--reuse-owner forces the canonical repo, --attach-existing attaches instead.
--worktree requires a repo (owner/repo).`,
		Example: `  # Spawn an agent from the 'mcp-node' template as-is (no repo)
  leo agent spawn mcp-node

  # Spawn against a specific repo with a dedicated git worktree
  leo agent spawn mcp-node owner/fetch --worktree feat/new-endpoint

  # Non-interactive: attach to the existing agent on collision
  leo agent spawn mcp-node leo --attach-existing`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateSpawnTemplate(cmd, args[0]); err != nil {
				return err
			}
			template := args[0]
			if len(args) == 2 {
				if repo != "" {
					return fmt.Errorf("repo given both as positional arg and --repo flag; pick one")
				}
				repo = args[1]
			}
			if reuseOwner && attachExisting {
				return fmt.Errorf("--reuse-owner and --attach-existing are mutually exclusive")
			}
			if branch != "" && repo == "" {
				return fmt.Errorf("--worktree requires a repo")
			}
			if branch != "" && !strings.Contains(repo, "/") {
				return fmt.Errorf("--worktree requires owner/repo; got %q", repo)
			}
			if base != "" && branch == "" {
				return fmt.Errorf("--base only applies with --worktree")
			}

			env, err := parseEnvPairs(envPairs)
			if err != nil {
				return err
			}

			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"spawn", template}
				if repo != "" {
					extra = append(extra, "--repo", repo)
				}
				if asJSON {
					extra = append(extra, "--json")
				}
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
				if prompt != "" {
					extra = append(extra, "--prompt", prompt)
				}
				for _, p := range envPairs {
					extra = append(extra, "--env", p)
				}
				if idleSuspend != "" {
					extra = append(extra, "--idle-suspend", idleSuspend)
				}
				return runRemote(res, extra)
			}

			// Collision detection: slashless repos match by repo short-name
			// (ambiguous owner), slashed repos match exactly on (Repo, Branch).
			// A repo-less spawn (template run as-is) has no short-name to
			// collide on — agentstore records with no Repo are never returned
			// by findRepoShortMatches — so skip the round trip entirely.
			switch {
			case repo == "":
				// No conflict — fall through and spawn.
			case !strings.Contains(repo, "/"):
				matches, err := findRepoShortMatches(cmd.Context(), cfg.HomePath, repo)
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
						return attachLocal(cmd.Context(), cmd, cfg.HomePath, matches[0].Name, attachOptions{})
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
			default:
				matches, err := findExactMatches(cmd.Context(), cfg.HomePath, repo, branch)
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
						return attachLocal(cmd.Context(), cmd, cfg.HomePath, matches[0].Name, attachOptions{})
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

			rec, err := daemon.AgentSpawn(cmd.Context(), cfg.HomePath, daemon.AgentSpawnRequest{
				Template:    template,
				Repo:        repo,
				Name:        name,
				Branch:      branch,
				Base:        base,
				Prompt:      prompt,
				Env:         env,
				IdleSuspend: idleSuspend,
			})
			if err != nil {
				return fmt.Errorf("spawning agent: %w", err)
			}
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rec)
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the spawned agent record as JSON")
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo to clone, or plain name for template workspace")
	cmd.Flags().StringVar(&name, "name", "", "override the derived agent name")
	cmd.Flags().StringVar(&branch, "worktree", "", "create a dedicated git worktree on this branch (requires owner/repo)")
	cmd.Flags().StringVar(&base, "base", "", "base ref for new branches (defaults to origin HEAD)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "opening prompt delivered as the agent's first interactive turn")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "extra env var as KEY=VALUE (repeatable); overrides template env on collision")
	cmd.Flags().BoolVar(&reuseOwner, "reuse-owner", false, "on collision, spawn using the existing agent's canonical owner/repo")
	cmd.Flags().BoolVar(&attachExisting, "attach-existing", false, "on collision, attach to the existing agent instead of spawning")
	cmd.Flags().StringVar(&idleSuspend, "idle-suspend", "", "suspend agent after this idle interval (e.g. \"24h\", \"30m\"); overrides template/defaults idle_suspend_after")
	return cmd
}

// parseEnvPairs converts repeated KEY=VALUE flag values into a map. Returns nil
// for an empty input so the "no env" case stays unset on the wire. The key must
// be non-empty; VALUE may contain '=' (only the first '=' splits).
func parseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(pairs))
	for _, p := range pairs {
		key, val, ok := strings.Cut(p, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --env %q: expected KEY=VALUE", p)
		}
		env[key] = val
	}
	return env, nil
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
func findRepoShortMatches(ctx context.Context, homePath, query string) ([]agent.Record, error) {
	records, err := daemon.AgentList(ctx, homePath)
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
func findExactMatches(ctx context.Context, homePath, repo, branch string) ([]agent.Record, error) {
	records, err := daemon.AgentList(ctx, homePath)
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
func attachLocal(ctx context.Context, cmd *cobra.Command, homePath, query string, opts attachOptions) error {
	session, err := lookupAgentSession(ctx, homePath, query)
	if err != nil {
		return fmt.Errorf("looking up session: %w", err)
	}

	// Dormant agents have no tmux session to attach to yet — prompt to start
	// (or fail fast off a TTY) before doing anything else, and BEFORE the
	// attach-spec lookup below. Shares ensureAgentRunning with the top-level
	// `leo attach` door so the prompt behaves identically from either entry
	// point.
	ok, err := ensureAgentRunning(ctx, cmd, homePath, session.Name, session.Stopped)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	// Non-claude agents have no tmux session to attach to — route through
	// their SessionDriver's AttachSpec instead. attach-spec returns an empty
	// Harness for claude agents (the overwhelming majority), so this call is
	// on the hot path; keep it a single fast round-trip. Looked up AFTER
	// ensureAgentRunning: ResolveHandle bails on a still-dormant record
	// (internal/agent/manager.go), so querying it before a start would
	// always see the empty/claude fallback for an agent that just needed
	// starting — matching attach.go's door, which has the same ordering. A
	// lookup failure silently fell through to the tmux attach below with no
	// clue for the user why they landed in the raw serve pane — warn on
	// stderr instead of swallowing the error, while still keeping the
	// fallback itself.
	if spec, err := agentAttachSpecFn(ctx, homePath, query); err == nil {
		if spec.Harness != "" && spec.Harness != "claude" {
			res := config.HostResolution{Localhost: true}
			return attachViaDriver(res, toAttachSpec(spec), opts)
		}
	} else {
		fmt.Fprintf(agentStderr, "warning: driver attach lookup failed (%v); falling back to tmux attach\n", err)
	}

	return attachTmuxSession(config.HostResolution{Localhost: true}, session.Session, opts)
}

// --- attach ---

func newAgentAttachCmd() *cobra.Command {
	var host string
	var cc bool
	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to the agent's tmux session",
		Long: `Attach to a running agent's session (tmux, or its harness driver's
attach TUI for non-claude agents). Locally and remotely this delegates to the
same routing logic, so a non-claude agent lands in its driver's attach view
instead of a raw tmux pane. Remotely (non --cc) this runs 'ssh -t <host>
<leo_path> agent attach <name>', letting the remote leo do that routing.
Detach with the usual tmux prefix + d (default: C-b d) for claude agents.

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
				// --cc (tmux control mode) must be interpreted by the LOCAL
				// terminal (iTerm2/WezTerm render it natively) — it cannot be
				// delegated to the remote side. Keep the old flow here: resolve
				// the shorthand through the remote daemon, then attach directly
				// to the tmux session with -CC (attachTmuxSession carries the
				// ssh -tt -e none handling this needs). A driver-attached agent
				// has no tmux UI to control-mode into anyway, so this is
				// claude/tmux-only by design (Plan 4 deferral).
				if cc {
					session, err := resolveRemoteSession(res, name)
					if err != nil {
						return err
					}
					return attachTmuxSession(res, session, attachOptions{cc: cc})
				}
				// Non-cc: delegate the whole `leo agent attach <name>` invocation
				// to the remote leo, same as top-level `leo attach` does — the
				// remote binary resolves the agent and routes through its own
				// SessionDriver (attachLocal) instead of us raw-tmux-attaching
				// and bypassing driver routing for non-claude harnesses.
				return runRemoteAttach(res, "agent", "attach", name)
			}

			return attachLocal(cmd.Context(), cmd, cfg.HomePath, name, attachOptions{cc: cc})
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
			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, query)
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

// --- delete-plan ---

// newAgentDeletePlanCmd is a scripting/plumbing subcommand: it prints what
// `leo agent delete` would remove, as JSON, without removing anything. It
// backs the attach picker's remote (SSH) backend, which needs this fact to
// render an accurate delete confirmation without a second daemon endpoint of
// its own — the local picker backend and the CLI's own delete prompt both
// call the daemon's delete-plan endpoint directly instead.
func newAgentDeletePlanCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:    "delete-plan <name>",
		Short:  "Print what 'leo agent delete' would remove, as JSON",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"delete-plan", name})
			}
			plan, err := daemon.AgentDeletePlan(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("planning delete: %w", err)
			}
			enc := json.NewEncoder(agentStdout)
			enc.SetIndent("", "  ")
			return enc.Encode(plan)
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// --- stop ---

func newAgentStopCmd() *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running agent",
		Long: `Stop a running agent's tmux session. The agent stays dormant — its
record (and worktree, if it has one) is kept so it can be started again later.`,
		Example:           `  leo agent stop leo-mcp-node-owner-fetch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"stop", name}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemote(res, extra)
			}

			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			canonical := resolved.Name

			if err := daemon.AgentStop(cmd.Context(), cfg.HomePath, canonical, false); err != nil {
				return fmt.Errorf("stopping agent: %w", err)
			}
			result := agentStopResult{Name: canonical, Stopped: true}

			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			fmt.Fprintf(agentStdout, "stopped %s\n", canonical)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the stop result as JSON")
	return cmd
}

// --- start ---

func newAgentStartCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Start a dormant (stopped) agent",
		Long: `Start a dormant agent, clearing its stopped flags and respawning it with
--resume so the prior conversation continues. Accepts shorthand — the same
resolution stop/rename/restart use.`,
		Example:           `  leo agent start pretty-sky`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"start", name})
			}

			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			if err := daemon.AgentStart(cmd.Context(), cfg.HomePath, resolved.Name); err != nil {
				return fmt.Errorf("starting agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "started %s\n", resolved.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// --- delete ---

func newAgentDeleteCmd() *cobra.Command {
	var host string
	var deleteBranch, force, assumeYes, asJSON bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a dormant agent's record (and worktree/branch, if it has one)",
		Long: `Permanently remove an agent's persisted record — plus its git worktree
(and, with --delete-branch, its branch) for a worktree agent. Deletion is the
only thing that removes a record; stopping never does. Refuses a live agent —
stop it first with 'leo agent stop'.

Without --yes, prompts for confirmation naming exactly what will be removed.
The prompt is skipped in non-interactive runs (no TTY); in that case the
command errors unless --yes is set.`,
		Example: `  # Delete a shared-workspace agent (no worktree)
  leo agent delete rocket

  # Delete a worktree agent and its branch, skipping the confirmation prompt
  leo agent delete pretty-sky --delete-branch --yes

  # Force past a dirty worktree or an unmerged branch
  leo agent delete pretty-sky --delete-branch --force`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"delete", name}
				if deleteBranch {
					extra = append(extra, "--delete-branch")
				}
				if force {
					extra = append(extra, "--force")
				}
				if assumeYes {
					extra = append(extra, "--yes")
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
			canonical := resolved.Name

			if !assumeYes {
				plan, err := daemon.AgentDeletePlan(cmd.Context(), cfg.HomePath, canonical)
				if err != nil {
					return fmt.Errorf("planning delete: %w", err)
				}
				if !agentIsTTY() {
					return fmt.Errorf("refusing to delete %s without confirmation: pass --yes to skip the prompt when stdin is not a TTY", canonical)
				}
				reader := bufio.NewReader(agentStdin)
				label := fmt.Sprintf("delete %s? %s", agent.DisplayName(canonical), plan.ConfirmText(deleteBranch))
				if !prompt.YesNo(reader, label, false) {
					fmt.Fprintln(agentStdout, "Aborted.")
					return nil
				}
			}

			if err := daemon.AgentDelete(cmd.Context(), cfg.HomePath, canonical, daemon.AgentDeleteRequest{
				Force:        force,
				DeleteBranch: deleteBranch,
			}); err != nil {
				return fmt.Errorf("deleting agent: %w", err)
			}
			result := agentDeleteResult{Name: canonical, Deleted: true}
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			fmt.Fprintf(agentStdout, "deleted %s\n", canonical)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "also delete the local branch (worktree agents only)")
	cmd.Flags().BoolVar(&force, "force", false, "remove even when the worktree is dirty or the branch is unmerged")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the delete result as JSON")
	return cmd
}

// agentDeleteResult is the JSON shape for `leo agent delete --json`.
type agentDeleteResult struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// --- reset ---

func newAgentResetCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "reset <name>",
		Short: "Reset an agent to a brand-new conversation",
		Long: `Reset an agent by stopping its process/tmux session, clearing its stored
claude session id, and respawning it fresh. Unlike 'leo agent start', which
rejoins the prior conversation, reset starts a brand-new one — use this when
an agent's context has gotten stuck or corrupted.`,
		Example:           `  leo agent reset leo-coding-owner-fetch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"reset", name})
			}
			// Resolve shorthand to canonical name (same resolution as stop/start).
			resolved, err := daemon.AgentResolve(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("resolving agent: %w", err)
			}
			if err := daemon.AgentReset(cmd.Context(), cfg.HomePath, resolved.Name); err != nil {
				return fmt.Errorf("resetting agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "reset %s\n", resolved.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// --- restart ---

// agentRestartAllResult is the JSON shape for `leo agent restart --all --json`.
type agentRestartAllResult struct {
	Restarted []string          `json:"restarted"`
	Skipped   []string          `json:"skipped"`
	Failed    map[string]string `json:"failed,omitempty"`
}

func newAgentRestartCmd() *cobra.Command {
	var host string
	var all, assumeYes, asJSON bool
	cmd := &cobra.Command{
		Use:   "restart [name]",
		Short: "Bounce a running agent's tmux session, preserving its conversation",
		Long: `Restart a running agent by killing its process/tmux session and
respawning it with --resume, so the conversation carries over. Unlike 'leo
agent reset', which starts a brand-new conversation, restart just bounces the
process — use this after a config/template change that needs a fresh process
but should keep context.

For an agent spawned from a template that still exists in the current config
with its harness unchanged, restart also re-applies today's defaults +
template config (e.g. an updated harness_options or model) before resuming —
not just the args it was originally spawned with. Ad-hoc agents, agents whose
template was deleted, and agents whose effective harness changed keep their
original args.

Pass a single agent name, or --all to bounce every currently-running agent
(stopped agents are skipped, not restarted).`,
		Example: `  # Bounce one agent
  leo agent restart leo-coding-owner-fetch

  # Bounce every running agent, skipping the confirmation prompt
  leo agent restart --all --yes`,
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("cannot combine an agent name with --all")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("requires exactly one agent name, or --all to restart every running agent")
			}
			return nil
		},
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"restart"}
				if all {
					extra = append(extra, "--all")
				} else {
					extra = append(extra, args[0])
				}
				if assumeYes {
					extra = append(extra, "--yes")
				}
				if asJSON {
					extra = append(extra, "--json")
				}
				return runRemote(res, extra)
			}

			if !all {
				name := args[0]
				// AgentRestart resolves shorthand server-side (with a store
				// fallback for a record stopped by a failed boot-time
				// restore that a plain resolve excludes) and echoes back the
				// canonical record, so no separate pre-resolve call is
				// needed here.
				resolved, err := daemon.AgentRestart(cmd.Context(), cfg.HomePath, name)
				if err != nil {
					return fmt.Errorf("restarting agent: %w", err)
				}
				if asJSON {
					enc := json.NewEncoder(agentStdout)
					enc.SetIndent("", "  ")
					return enc.Encode(agentRestartAllResult{Restarted: []string{resolved.Name}})
				}
				fmt.Fprintf(agentStdout, "restarted %s\n", resolved.Name)
				return nil
			}

			if !assumeYes {
				if !agentIsTTY() {
					return fmt.Errorf("refusing to restart every running agent without confirmation: pass --yes to skip the prompt when stdin is not a TTY")
				}
				reader := bufio.NewReader(agentStdin)
				if !prompt.YesNo(reader, "Restart every running agent?", false) {
					fmt.Fprintln(agentStdout, "Aborted.")
					return nil
				}
			}

			result, err := daemon.AgentRestartAll(cmd.Context(), cfg.HomePath)
			if err != nil {
				return fmt.Errorf("restarting agents: %w", err)
			}

			if asJSON {
				failed := make(map[string]string, len(result.Failed))
				for name, ferr := range result.Failed {
					failed[name] = ferr.Error()
				}
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(agentRestartAllResult{
					Restarted: result.Restarted,
					Skipped:   result.Skipped,
					Failed:    failed,
				}); err != nil {
					return err
				}
				if len(result.Failed) > 0 {
					return fmt.Errorf("%d agent(s) failed to restart", len(result.Failed))
				}
				return nil
			}

			for _, name := range result.Restarted {
				fmt.Fprintf(agentStdout, "restarted %s\n", name)
			}
			for _, name := range result.Skipped {
				fmt.Fprintf(agentStdout, "skipped %s (not running)\n", name)
			}
			for name, ferr := range result.Failed {
				fmt.Fprintf(agentStdout, "failed %s: %v\n", name, ferr)
			}
			total := len(result.Restarted) + len(result.Skipped) + len(result.Failed)
			fmt.Fprintf(agentStdout, "restarted %d of %d\n", len(result.Restarted), total)
			if len(result.Failed) > 0 {
				return fmt.Errorf("%d agent(s) failed to restart", len(result.Failed))
			}
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&all, "all", false, "restart every currently-running agent (skips stopped agents)")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt when using --all")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the restart result as JSON")
	return cmd
}

// --- rename ---

func newAgentRenameCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "rename <name> <new-name>",
		Short: "Rename an agent",
		Long: `Rename an agent's identity. A running agent is renamed in place with no
process restart; its claude session keeps running. The new name is normalized to
a leo- prefixed slug (lowercase, a-z 0-9 and dashes only).`,
		Example:           `  leo agent rename leo-mcp-node-owner-fetch auth-refactor`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			name, newName := args[0], args[1]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"rename", name, newName})
			}
			updated, err := daemon.AgentRename(cmd.Context(), cfg.HomePath, name, newName)
			if err != nil {
				return fmt.Errorf("renaming agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "renamed %s -> %s\n", name, updated.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// agentStopResult is the JSON shape for `leo agent stop --json`.
type agentStopResult struct {
	Name    string `json:"name"`
	Stopped bool   `json:"stopped"`
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

			output, err := daemon.AgentLogs(cmd.Context(), cfg.HomePath, name, lines)
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
// completeAgentRemoteTimeout bounds the ssh round-trip for remote agent-name
// completion so a hung connection never blocks the shell. Var so tests can
// shrink it.
var completeAgentRemoteTimeout = 3 * time.Second

// completeAgentRemoteWaitDelay bounds cmd.Wait() after a Kill on timeout: if a
// child (or something it spawned) still holds the stdout pipe open, Wait can
// hang indefinitely waiting for the pipe to close even though the process
// itself is dead. See project_ci_cross_platform_tmux_exec.md for the prior
// exec-Wait-hangs-after-cancel finding this mirrors.
var completeAgentRemoteWaitDelay = 1 * time.Second

func completeAgentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx := context.Background()
	var flagHost string
	if cmd != nil {
		if cmdCtx := cmd.Context(); cmdCtx != nil {
			ctx = cmdCtx
		}
		flagHost, _ = cmd.Flags().GetString("host")
	}
	res, err := cfg.ResolveHost(flagHost)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if !res.Localhost {
		return completeAgentNamesRemote(ctx, res, toComplete)
	}
	records, err := agentListFn(ctx, cfg.HomePath)
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

// completeAgentNamesRemote delegates completion to the remote leo binary via
// its own `__complete agent attach <toComplete>` invocation, reusing cobra's
// completion wire format rather than inventing a listing contract — every
// agent-name completer offers the identical candidate set, so this one
// canonical remote invocation serves them all.
//
// ssh flattens post-host argv into a single string re-parsed by the remote
// login shell, so toComplete — which is frequently empty or partial — is
// shell-quoted before being handed to ssh; every other token here is a
// static literal with no shell metacharacters. No -t (no TTY needed) and a
// bounded context so a hung connection can't block the shell.
//
// Uses buildSSHArgs (host, SSHArgs, sshControlOpts) so completion reuses the
// same multiplexed ControlMaster connection as every other host dispatch
// instead of paying a fresh handshake per tab press.
func completeAgentNamesRemote(ctx context.Context, res config.HostResolution, toComplete string) ([]string, cobra.ShellCompDirective) {
	tail := []string{res.Host.RemoteLeoPath(), "__complete", "agent", "attach", shellQuoteArg(toComplete)}
	args := append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=2"}, buildSSHArgs(res, tail...)...)
	cmd := agentExecCommand("ssh", args...)
	cmd.WaitDelay = completeAgentRemoteWaitDelay
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// stderr intentionally left nil — "Completion ended with directive"
	// diagnostics from the remote go there and must not pollute stdout.

	if err := cmd.Start(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	cctx, cancel := context.WithTimeout(ctx, completeAgentRemoteTimeout)
	defer cancel()

	select {
	case err := <-done:
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	case <-cctx.Done():
		// cmd.WaitDelay bounds the Wait below even if a child still holds
		// the stdout pipe open after Kill.
		_ = cmd.Process.Kill()
		<-done
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return parseCompletionCandidates(stdout.String()), cobra.ShellCompDirectiveNoFileComp
}

// parseCompletionCandidates extracts candidate names from cobra's
// `__complete` wire output: one candidate per line (optionally suffixed with
// a tab-separated description cobra drops when rendering plain lists), then
// a trailing ":<directive>" line that this function discards along with any
// blank lines.
func parseCompletionCandidates(output string) []string {
	lines := strings.Split(output, "\n")
	candidates := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if idx := strings.Index(line, "\t"); idx >= 0 {
			line = line[:idx]
		}
		candidates = append(candidates, line)
	}
	return candidates
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
