package cli

import (
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

// Testability seam — overridden in tests.
var (
	agentExecCommand           = exec.Command
	agentSyscallExec           = syscall.Exec
	agentStderr      io.Writer = os.Stderr
	agentStdout      io.Writer = os.Stdout
)

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
	cmd := &cobra.Command{
		Use:   "spawn <template>",
		Short: "Spawn a new agent from a template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			template := args[0]
			if repo == "" {
				return fmt.Errorf("--repo is required (use owner/repo to clone, or a plain name to reuse the template workspace)")
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
				return runRemote(res, extra)
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
	return cmd
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
				// Remote: confirm the agent exists (friendlier error) then SSH -t attach.
				// We don't proxy through `ssh host leo agent attach` because that would
				// require another tmux nesting level.
				session, err := resolveRemoteSession(res, name)
				if err != nil {
					return err
				}
				sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
				sshArgs = append(sshArgs, "tmux", "attach", "-t", session)
				c := agentExecCommand("ssh", sshArgs...)
				c.Stdin = os.Stdin
				c.Stdout = agentStdout
				c.Stderr = agentStderr
				return c.Run()
			}

			session, err := daemon.AgentSession(cfg.HomePath, name)
			if err != nil {
				return fmt.Errorf("looking up session: %w", err)
			}
			tmuxPath, err := exec.LookPath("tmux")
			if err != nil {
				return fmt.Errorf("tmux not found in PATH: %w", err)
			}
			// Replace the CLI process so tmux owns the TTY cleanly. Returns an error
			// only if exec itself fails; on success this call does not return.
			return agentSyscallExec(tmuxPath, []string{"tmux", "attach", "-t", session}, os.Environ())
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// resolveRemoteSession shells `ssh <host> leo agent session-name <name>` to learn
// the tmux session name without attaching. For now it just derives it locally
// (leo-<name>) since the daemon uses a stable naming scheme — verifying the
// agent exists is left to the subsequent `tmux attach` call, which returns a
// friendly "no sessions" message on failure.
func resolveRemoteSession(_ config.HostResolution, name string) (string, error) {
	return "leo-" + name, nil
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
				session := "leo-" + name
				tailCmd := fmt.Sprintf("tmux capture-pane -t %s -p -S -%d; tmux pipe-pane -t %s 'cat >> /tmp/%s.log' 2>/dev/null; tail -f /tmp/%s.log",
					session, lines, session, session, session)
				if res.Localhost {
					c := agentExecCommand("sh", "-c", tailCmd)
					c.Stdin = os.Stdin
					c.Stdout = agentStdout
					c.Stderr = agentStderr
					return c.Run()
				}
				sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
				sshArgs = append(sshArgs, tailCmd)
				c := agentExecCommand("ssh", sshArgs...)
				c.Stdin = os.Stdin
				c.Stdout = agentStdout
				c.Stderr = agentStderr
				return c.Run()
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
