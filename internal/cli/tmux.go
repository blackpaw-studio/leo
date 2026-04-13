package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
)

// tmuxLookPath is a testability seam for locating the tmux binary. Tests
// override it so the local-attach path doesn't require tmux to exist on the
// runner (notably the macOS GitHub runner).
var tmuxLookPath = exec.LookPath

// attachTmuxSession replaces the current process with a tmux attach (local) or
// runs `ssh -t <host> <tmux> attach -t <session>` remotely. Session names are
// supplied fully-qualified (e.g. "leo-my-process") — callers are responsible for
// resolving the name. Returns an error only on exec/dispatch failure; on a
// successful local attach this call does not return.
func attachTmuxSession(res config.HostResolution, session string) error {
	if !res.Localhost {
		sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
		sshArgs = append(sshArgs, res.Host.RemoteTmuxPath(), "attach", "-t", session)
		c := agentExecCommand("ssh", sshArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return c.Run()
	}

	tmuxPath, err := tmuxLookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}
	// Replace the CLI process so tmux owns the TTY cleanly. Returns an error
	// only if exec itself fails; on success this call does not return.
	return agentSyscallExec(tmuxPath, []string{"tmux", "attach", "-t", session}, os.Environ())
}

// captureTmuxPane runs a one-shot `tmux capture-pane -p -S -<lines>` against
// the given session and writes output to the shared agentStdout. Local and
// remote paths share identical shape — remote just wraps through ssh with the
// host's configured tmux path.
func captureTmuxPane(res config.HostResolution, session string, lines int) error {
	if res.Localhost {
		return runShellCmd("tmux", []string{"capture-pane", "-t", session, "-p", "-S", fmt.Sprintf("-%d", lines)})
	}
	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, res.Host.RemoteTmuxPath(), "capture-pane", "-t", session, "-p", "-S", fmt.Sprintf("-%d", lines))
	return runShellCmd("ssh", sshArgs)
}

// followTmuxSession streams tmux pane output via `tail -f` on a pipe-pane log.
// Used by `leo agent logs -f` and `leo process logs -f`. When res is remote, it
// shells through ssh and uses the host's configured tmux path.
func followTmuxSession(res config.HostResolution, session string, lines int) error {
	buildTailCmd := func(tmuxCmd string) string {
		return fmt.Sprintf("%s capture-pane -t %s -p -S -%d; %s pipe-pane -t %s 'cat >> /tmp/%s.log' 2>/dev/null; tail -f /tmp/%s.log",
			tmuxCmd, session, lines, tmuxCmd, session, session, session)
	}
	if res.Localhost {
		return runShellCmd("sh", []string{"-c", buildTailCmd("tmux")})
	}
	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, buildTailCmd(res.Host.RemoteTmuxPath()))
	return runShellCmd("ssh", sshArgs)
}

// runShellCmd is a tiny wrapper that wires stdio to the package-level streams
// so tests can capture output. Uses agentExecCommand so both helpers share a
// single testability seam.
func runShellCmd(name string, args []string) error {
	c := agentExecCommand(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return c.Run()
}
