package cli

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// Testability seam — tests override this to simulate the daemon's view of
// running agents without spinning up a real socket.
var lookupAgentSession = daemon.AgentSession

// newAttachCmd registers a top-level `leo attach <name>` shortcut that
// disambiguates between configured processes and running agents. When the name
// exists in both namespaces, Leo refuses to guess — the user must use the
// explicit `leo process attach` or `leo agent attach` form.
//
// Calling `leo attach` with no arguments opens an interactive picker over the
// available sessions (local or remote) so you don't have to remember names.
func newAttachCmd() *cobra.Command {
	var host string
	var cc bool
	cmd := &cobra.Command{
		Use:   "attach [name]",
		Short: "Attach to a supervised process or running agent",
		Long: `Shortcut for 'leo process attach' or 'leo agent attach'. The name
is resolved against both namespaces — if it matches exactly one, Leo attaches
there. If both namespaces contain the name, Leo errors and asks you to use the
explicit subcommand.

When --host targets a remote, the resolution is delegated to the server so the
client does not need to know the remote's process list.

Passing no name opens an interactive arrow-key picker over the available
processes and agents. Pass --cc in a tmux-aware terminal (iTerm2, WezTerm) to
render the session as a native tab via tmux control mode.`,
		Example: `  # Attach to a configured process or running agent by name
  leo attach coding-assistant

  # Target a specific remote host from client.hosts
  leo attach fetch --host prod

  # No name — pick interactively
  leo attach`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			opts := attachOptions{cc: cc}

			if len(args) == 0 {
				return runAttachPicker(cmd.Context(), cfg, res, opts)
			}
			name := args[0]

			// Remote: hand the whole `leo attach <name>` invocation to the server so
			// it can resolve ambiguity with its own view of processes+agents.
			if !res.Localhost {
				return runRemoteAttach(res, name)
			}

			_, isProcess := cfg.Processes[name]
			// AgentSession is the authoritative presence check: the daemon only
			// returns a session for agents the agentstore knows about.
			var agentSession string
			if session, err := lookupAgentSession(cmd.Context(), cfg.HomePath, name); err == nil && session != "" {
				agentSession = session
			}

			switch {
			case isProcess && agentSession != "":
				return fmt.Errorf("%q is both a process and an agent — use 'leo process attach %s' or 'leo agent attach %s'", name, name, name)
			case isProcess:
				return attachTmuxSession(res, processSessionName(name), opts)
			case agentSession != "":
				return attachTmuxSession(res, agentSession, opts)
			default:
				return fmt.Errorf("no process or agent named %q", name)
			}
		},
	}
	addHostFlag(cmd, &host)
	addControlModeFlag(cmd, &cc)
	return cmd
}

// runRemoteAttach shells `ssh -t <host> <leo_path> attach <name>`. We keep the
// TTY flag so the remote tmux attach inherits it cleanly.
func runRemoteAttach(res config.HostResolution, name string) error {
	sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, res.Host.RemoteLeoPath(), "attach", name)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return c.Run()
}
