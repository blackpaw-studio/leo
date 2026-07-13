package cli

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/spf13/cobra"
)

// agentAttachSpecFn is a testability seam for daemon.AgentAttachSpec — tests
// override this to simulate the daemon's view of an agent's harness/attach
// spec without spinning up a real socket.
var agentAttachSpecFn = daemon.AgentAttachSpec

// toAttachSpec converts a daemon.AgentAttachSpecResponse to the
// harness.AttachSpec shape attachViaDriver consumes, carrying every field
// (including the tmux flavor) across the daemon/CLI boundary unchanged.
func toAttachSpec(spec daemon.AgentAttachSpecResponse) harness.AttachSpec {
	return harness.AttachSpec{
		Argv:        spec.Argv,
		HistoryPath: spec.HistoryPath,
		TmuxSession: spec.TmuxSession,
		WindowName:  spec.WindowName,
		WindowCmd:   spec.WindowCmd,
		WindowKey:   spec.WindowKey,
	}
}

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
				return runRemoteAttach(res, "attach", name)
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
				// Non-claude harnesses have no tmux session to attach to —
				// route through the driver instead, same as `leo process
				// attach` (resolveProcessAttachSpec is localhost-only, which
				// this branch always is: remote dispatch returned above).
				if _, spec, ok, err := resolveProcessAttachSpec(cfg, name); err != nil {
					return err
				} else if ok {
					return attachViaDriver(res, spec, opts)
				}
				return attachTmuxSession(res, processSessionName(name), opts)
			case agentSession != "":
				// Same non-claude routing as `leo agent attach` (attachLocal):
				// ask the daemon for the agent's harness/attach spec before
				// falling back to the tmux session already resolved above.
				if spec, err := agentAttachSpecFn(cmd.Context(), cfg.HomePath, name); err == nil && spec.Harness != "" && spec.Harness != "claude" {
					return attachViaDriver(res, toAttachSpec(spec), opts)
				}
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

// runRemoteAttach shells `ssh -t <host> <leo_path> <remoteArgs...>`, delegating
// the whole invocation to the remote leo binary so it can do its own
// resolution (and, for `agent attach`, its own driver routing — see
// newAgentAttachCmd). We keep the TTY flag so the remote tmux attach inherits
// it cleanly.
//
// remoteArgs is the subcommand and args to run on the remote leo, e.g.
// ("attach", name) for top-level attach or ("agent", "attach", name) for
// `leo agent attach`.
func runRemoteAttach(res config.HostResolution, remoteArgs ...string) error {
	// The remote leo will re-resolve to a local tmux attach, which inherits
	// $TERM from this SSH session. Make sure the remote knows that terminal
	// type — or fall back to xterm-256color on the remote command.
	termOverride := ensureRemoteTerminfoFn(res)
	sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
	prefixLen := len(sshArgs)
	sshArgs = append(sshArgs, res.Host.RemoteLeoPath())
	sshArgs = append(sshArgs, remoteArgs...)
	sshArgs = applyRemoteTermFallback(sshArgs, prefixLen, termOverride)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return c.Run()
}
