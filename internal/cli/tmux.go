package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// tmuxLocate is a testability seam for locating the tmux binary. Tests
// override it so the local-attach path doesn't require tmux on the runner
// (notably the macOS GitHub runner). It defaults to tmux.Locate, which
// checks $PATH then a small set of well-known install locations — needed
// because leo's local-attach branch also runs on the remote side when the
// top-level `leo attach` is dispatched over SSH (the non-interactive shell
// usually does not have /opt/homebrew/bin on PATH).
var tmuxLocate = tmux.Locate

// tmuxEnv reads $TMUX; indirected so tests can simulate being inside or
// outside a tmux client without actually nesting one.
var tmuxEnv = func() string { return os.Getenv("TMUX") }

// attachOptions configures the attach-flavor flags (currently just tmux
// control mode). Extending this struct is cheaper than threading parallel
// bool args through callers as new flags land.
type attachOptions struct {
	// cc enables tmux control mode (`-CC`) so terminals like iTerm2 and
	// WezTerm render the attached session as native tabs. Local-only —
	// the ssh + -tty route out of control-mode territory is messy enough
	// that we reject it up front.
	cc bool
}

// attachTmuxSession replaces the current process with a tmux attach (local) or
// runs `ssh -t <host> <tmux> -L leo attach -t <session>` remotely. Session names
// are supplied fully-qualified (e.g. "leo-my-process") — callers are responsible
// for resolving the name. Returns an error only on exec/dispatch failure; on a
// successful local attach this call does not return.
//
// When the caller is already inside tmux ($TMUX set), Leo uses
// `display-popup -E` on the user's current tmux to open an overlay that runs
// the leo-socket attach. This keeps the user's outer tmux intact and avoids
// nesting a second full tmux client inside the first.
func attachTmuxSession(res config.HostResolution, session string, opts attachOptions) error {
	if !res.Localhost {
		if opts.cc {
			return fmt.Errorf("--cc (tmux control mode) is local-only; it is not supported over SSH")
		}
		sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
		sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
		sshArgs = append(sshArgs, tmux.Args("attach", "-t", session)...)
		c := agentExecCommand("ssh", sshArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return c.Run()
	}

	tmuxPath, err := tmuxLocate()
	if err != nil {
		return err
	}

	// Inside a different tmux server (the user's personal socket) we can't
	// switch-client across sockets. Use display-popup on the outer server to
	// spawn an overlay running `tmux -L leo attach`. Dismissing the popup
	// returns control to the user's original session untouched.
	if opts.cc {
		// display-popup runs its own tmux client; -CC on top of a popup is
		// meaningless, so require the outer context to be a clean terminal.
		if tmuxEnv() != "" {
			return fmt.Errorf("--cc requires a non-tmux terminal; detach first (prefix+d) and retry")
		}
		argv := append([]string{"tmux"}, tmux.Args("-CC", "attach", "-t", session)...)
		return agentSyscallExec(tmuxPath, argv, os.Environ())
	}
	if tmuxEnv() != "" {
		inner := fmt.Sprintf("%s -L %s attach -t %s", shellQuoteArg(tmuxPath), tmux.SocketName, shellQuoteArg(session))
		popupArgs := []string{"display-popup", "-E", "-w", "95%", "-h", "95%", inner}
		c := agentExecCommand(tmuxPath, popupArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return c.Run()
	}
	// Replace the CLI process so tmux owns the TTY cleanly. Returns an error
	// only if exec itself fails; on success this call does not return.
	argv := append([]string{"tmux"}, tmux.Args("attach", "-t", session)...)
	return agentSyscallExec(tmuxPath, argv, os.Environ())
}

// shellQuoteArg wraps a value in single quotes, escaping any embedded single
// quotes, so it can be safely embedded in a tmux display-popup command string.
// Paths and session names pass through `tmux display-popup -E "<cmd>"`, which
// hands the string to `/bin/sh -c`, so shell-quoting is required.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// captureTmuxPane runs a one-shot `tmux capture-pane -p -S -<lines>` against
// the given session and writes output to the shared agentStdout. Local and
// remote paths share identical shape — remote just wraps through ssh with the
// host's configured tmux path.
func captureTmuxPane(res config.HostResolution, session string, lines int) error {
	subArgs := tmux.Args("capture-pane", "-t", session, "-p", "-S", fmt.Sprintf("-%d", lines))
	if res.Localhost {
		tmuxPath, err := tmuxLocate()
		if err != nil {
			return err
		}
		return runShellCmd(tmuxPath, subArgs)
	}
	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
	sshArgs = append(sshArgs, subArgs...)
	return runShellCmd("ssh", sshArgs)
}

// followTmuxSession streams tmux pane output via `tail -f` on a pipe-pane log.
// Used by `leo agent logs -f` and `leo process logs -f`. When res is remote, it
// shells through ssh and uses the host's configured tmux path.
func followTmuxSession(res config.HostResolution, session string, lines int) error {
	buildTailCmd := func(tmuxCmd string) string {
		return fmt.Sprintf("%s -L %s capture-pane -t %s -p -S -%d; %s -L %s pipe-pane -t %s 'cat >> /tmp/%s.log' 2>/dev/null; tail -f /tmp/%s.log",
			tmuxCmd, tmux.SocketName, session, lines,
			tmuxCmd, tmux.SocketName, session, session, session)
	}
	if res.Localhost {
		// The embedded tmux invocation runs under `sh -c`, whose PATH may not
		// include /opt/homebrew/bin when leo itself was launched from a
		// stripped environment. Resolve to an absolute path up front.
		tmuxPath, err := tmuxLocate()
		if err != nil {
			return err
		}
		return runShellCmd("sh", []string{"-c", buildTailCmd(tmuxPath)})
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
