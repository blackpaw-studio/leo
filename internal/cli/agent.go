package cli

import (
	"bufio"
	"encoding/json"
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
func agentSessionName(name string) string { return "leo-" + name }

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
		newAgentLogsCmd(),
		newAgentSessionNameCmd(),
	)
	return cmd
}

// hostFlag is the --host value, shared across subcommands.
func addHostFlag(cmd *cobra.Command, host *string) {
	cmd.Flags().StringVar(host, "host", "", `remote host name (from client.hosts), or "localhost"`)
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

// runRemote executes `ssh <host> <leo_path> agent <subcmd args...>` forwarding
// stdio. The remote binary path comes from HostConfig.LeoPath or defaults to
// config.DefaultRemoteLeoPath — SSH's non-interactive shell typically doesn't
// source .zshrc, so relying on bare "leo" in PATH is fragile.
func runRemote(res config.HostResolution, subcmdArgs []string) error {
	args := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	args = append(args, res.Host.RemoteLeoPath(), "agent")
	args = append(args, subcmdArgs...)
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
			fmt.Fprintln(tw, "NAME\tTEMPLATE\tWORKSPACE\tSTATUS\tRESTARTS")
			for _, r := range records {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
					r.Name, dashIfEmpty(r.Template), dashIfEmpty(r.Workspace),
					dashIfEmpty(r.Status), r.Restarts)
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
	var host, repo, name string
	var reuseOwner, attachExisting bool
	cmd := &cobra.Command{
		Use:   "spawn <template> [repo]",
		Short: "Spawn a new agent from a template",
		Long: `Spawn a new ephemeral agent from a template. Repo can be passed as a
positional arg or via --repo. Use owner/repo to clone a canonical repo, or a
plain name to reuse the template workspace.

When repo is slashless and matches an existing agent's short name, the CLI
prompts the user for how to proceed: attach to the existing agent, spawn using
that agent's canonical owner/repo, or spawn a fresh template workspace. The
prompt is skipped in non-interactive runs (no TTY). Flags override the prompt:
--reuse-owner forces the canonical repo, --attach-existing attaches instead.`,
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
				return runRemote(res, extra)
			}

			// Collision detection only applies to slashless repos. owner/repo
			// input is explicit and unambiguous by construction.
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
						return attachLocal(cfg.HomePath, matches[0].Name)
					case spawnUseCanonicalRepo:
						repo = matches[0].Repo
					case spawnFreshTemplate:
						// fall through unchanged
					case spawnCancel:
						return fmt.Errorf("spawn cancelled")
					}
				default:
					labels := make([]string, 0, len(matches))
					for _, m := range matches {
						labels = append(labels, fmt.Sprintf("%s (%s)", m.Name, m.Repo))
					}
					return fmt.Errorf("multiple existing agents match %q: %s — pass the full owner/repo to disambiguate",
						repo, strings.Join(labels, ", "))
				}
			}

			rec, err := daemon.AgentSpawn(cfg.HomePath, daemon.AgentSpawnRequest{
				Template: template,
				Repo:     repo,
				Name:     name,
			})
			if err != nil {
				return fmt.Errorf("spawning agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "spawned %s (workspace: %s)\n", rec.Name, rec.Workspace)
			fmt.Fprintf(agentStdout, "attach with: leo agent attach %s\n", rec.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo to clone, or plain name for template workspace")
	cmd.Flags().StringVar(&name, "name", "", "override the derived agent name")
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
func findRepoShortMatches(homePath, query string) ([]agent.Record, error) {
	records, err := daemon.AgentList(homePath)
	if err != nil {
		return nil, err
	}
	var out []agent.Record
	for _, r := range records {
		short := repoShortCLI(r.Repo)
		if short == "" {
			continue
		}
		if strings.EqualFold(short, query) {
			out = append(out, r)
		}
	}
	return out, nil
}

// repoShortCLI mirrors agent.shortRepo — kept private here to avoid exporting
// a 5-line helper across package boundaries.
func repoShortCLI(repo string) string {
	if repo == "" {
		return ""
	}
	if idx := strings.Index(repo, "/"); idx >= 0 {
		return repo[idx+1:]
	}
	return repo
}

// resolveSpawnCollision decides what to do when a slashless repo query matches
// exactly one existing agent. Flags force a non-interactive choice; otherwise
// the user is prompted when a TTY is attached. Non-interactive runs default
// to "fresh template" to preserve the current behavior for scripts and the
// web UI.
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
		return spawnFreshTemplate, nil
	}

	fmt.Fprintf(agentStderr, "\nAn agent already targets %s:\n", match.Repo)
	fmt.Fprintf(agentStderr, "  name:     %s\n", match.Name)
	fmt.Fprintf(agentStderr, "  template: %s\n\n", dashIfEmpty(match.Template))
	fmt.Fprintln(agentStderr, "  a) attach to the existing agent")
	if match.Repo != "" {
		fmt.Fprintf(agentStderr, "  b) spawn a new agent using that canonical repo (%s)\n", match.Repo)
	}
	fmt.Fprintf(agentStderr, "  c) spawn a fresh agent under template %q (current behavior)\n", template)
	fmt.Fprintln(agentStderr, "  q) cancel")
	fmt.Fprint(agentStderr, "\nchoice [c]: ")

	reader := bufio.NewReader(agentStdin)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
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
		return spawnCancel, nil
	default:
		return spawnCancel, fmt.Errorf("unknown choice %q", strings.TrimSpace(line))
	}
}

// attachLocal performs the local tmux-attach flow: look up the canonical
// session via the daemon, then hand off to tmux with syscall.Exec so the TTY
// owner is tmux itself. Shared between `leo agent attach` and the collision
// prompt's "attach-existing" branch.
func attachLocal(homePath, query string) error {
	session, err := daemon.AgentSession(homePath, query)
	if err != nil {
		return fmt.Errorf("looking up session: %w", err)
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}
	return agentSyscallExec(tmuxPath, []string{"tmux", "attach", "-t", session}, os.Environ())
}

// --- attach ---

func newAgentAttachCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to the agent's tmux session",
		Long: `Attach to a running agent's tmux session. Locally this replaces the
current process with tmux so the TUI has full control of the terminal.
Remotely it runs 'ssh -t <host> tmux attach -t leo-<name>'. Detach with
the usual tmux prefix + d (default: C-b d).`,
		Args: cobra.ExactArgs(1),
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
				return attachTmuxSession(res, session)
			}

			return attachLocal(cfg.HomePath, name)
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// resolveRemoteSession shells `ssh <host> leo agent session-name <query>` to
// ask the remote daemon for the canonical tmux session. Going through the
// daemon lets the user pass shorthand (plain repo name, short suffix) over SSH
// and surface clear "no match" / "ambiguous" errors before the tmux attach.
func resolveRemoteSession(res config.HostResolution, query string) (string, error) {
	args := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	args = append(args, res.Host.RemoteLeoPath(), "agent", "session-name", query)
	cmd := agentExecCommand("ssh", args...)
	cmd.Stderr = agentStderr
	out, err := cmd.Output()
	if err != nil {
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
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemote(res, []string{"stop", name})
			}
			if err := daemon.AgentStop(cfg.HomePath, name); err != nil {
				return fmt.Errorf("stopping agent: %w", err)
			}
			fmt.Fprintf(agentStdout, "stopped %s\n", name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
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
		Args:  cobra.ExactArgs(1),
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
