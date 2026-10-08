package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

// hintRemoteTmuxMissing enriches a remote tmux failure with actionable
// guidance. ssh relays the remote command's exit status, so a remote `tmux`
// that isn't on the non-interactive SSH PATH surfaces as exit 127 ("command
// not found"). This is the common case on Homebrew macOS, where tmux lives in
// /opt/homebrew/bin — added to PATH by a login profile that `ssh host cmd`
// does not source. The fix is the per-host `tmux_path` setting, but the bare
// exit-127 gives the user no clue; point them at it. Non-127 errors and
// localhost paths pass through untouched.
func hintRemoteTmuxMissing(res config.HostResolution, err error) error {
	if err == nil || res.Localhost {
		return err
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		return err
	}
	return fmt.Errorf("%w\n\nremote tmux (%q) was not found on %s's non-interactive SSH PATH. "+
		"Set tmux_path for this host in leo.yaml to its absolute path, e.g.:\n"+
		"  client.hosts.%s.tmux_path: /opt/homebrew/bin/tmux",
		err, res.Host.RemoteTmuxPath(), res.Name, res.Name)
}

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
	// WezTerm render the attached session as native tabs. Local attaches exec
	// `tmux -CC`; remote attaches stream it over SSH (see
	// attachRemoteControlMode).
	cc bool
	// readOnly attaches with `-r`: the client can look but its keystrokes
	// never reach the session's panes.
	readOnly bool
	// asChild runs the local attach as a child process (stdio inherited, exit
	// status passed through) instead of replacing leo with tmux, for callers
	// that must clean up after the client leaves or fails to start.
	asChild bool
	// dispatchPlacement (pane, window or background) asks the daemon to open
	// dispatch viewers that way while this client is attached; see
	// registerAttachPlacement. Empty leaves placement to the session and
	// config defaults.
	dispatchPlacement string
	// homePath is the leo home whose daemon the placement registers with.
	homePath string
}

// attachPlacementRegisterFn is the testability seam for registering a
// per-attach dispatch placement with the daemon.
var attachPlacementRegisterFn = daemon.RegisterAttachPlacement

// attachPlacementRegisterTimeout bounds the registration so a wedged daemon
// cannot hold up an attach.
const attachPlacementRegisterTimeout = 3 * time.Second

// registerAttachPlacement tells the daemon this process is about to become a
// tmux client wanting opts.dispatchPlacement. exec keeps the pid, so the
// registered pid is the client's #{client_pid}. An unreachable daemon costs
// the placement, not the attach.
func registerAttachPlacement(opts attachOptions, session string) {
	if opts.dispatchPlacement == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), attachPlacementRegisterTimeout)
	defer cancel()
	if err := attachPlacementRegisterFn(ctx, opts.homePath, session, os.Getpid(), opts.dispatchPlacement); err != nil {
		fmt.Fprintf(agentStderr, "warning: --dispatch-placement not registered (%v); attaching anyway\n", err)
	}
}

// validateDispatchPlacement checks a --dispatch-placement value; empty means
// the flag was not given.
func validateDispatchPlacement(value string) error {
	if value == "" || config.IsDispatchViewerPlacement(value) {
		return nil
	}
	return fmt.Errorf("invalid --dispatch-placement %q: want pane, window or background", value)
}

// addDispatchPlacementFlag registers --dispatch-placement on an attach command.
func addDispatchPlacementFlag(cmd *cobra.Command, placement *string) {
	cmd.Flags().StringVar(placement, "dispatch-placement", "", "open dispatch viewers as pane, window or background while this client is attached")
}

// runRemoteAttachPlaced attaches on a remote host through the remote leo, so
// the --dispatch-placement registration happens on the daemon's own host.
// head is the remote subcommand ("attach" or "agent", "attach"). Control mode
// needs its own ssh flags (see attachRemoteControlMode); otherwise this is
// runRemoteAttach.
func runRemoteAttachPlaced(res config.HostResolution, opts attachOptions, head []string, name string) error {
	remoteArgs := append([]string{}, head...)
	if opts.cc {
		remoteArgs = append(remoteArgs, "--cc")
	}
	if opts.dispatchPlacement != "" {
		remoteArgs = append(remoteArgs, "--dispatch-placement", opts.dispatchPlacement)
	}
	// "--" keeps a name that starts with "-" from being read as a flag, and
	// the remote login shell re-parses everything ssh sends, so quote what a
	// shell would mangle.
	remoteArgs = append(remoteArgs, "--", remoteShellWord(name))
	if !opts.cc {
		return runRemoteAttach(res, remoteArgs...)
	}
	sshArgs := []string{"-tt", "-e", "none", res.Host.SSH}
	sshArgs = append(sshArgs, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, res.Host.RemoteLeoPath())
	sshArgs = append(sshArgs, remoteArgs...)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return c.Run()
}

// attachSignalSource is the testability seam for the signals leo forwards to
// an attach child: it returns the delivery channel and a func that stops
// delivery.
var attachSignalSource = func() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return ch, func() { signal.Stop(ch) }
}

// runForwardingSignals runs c to completion, forwarding SIGINT, SIGTERM and
// SIGHUP to it meanwhile. leo keeps waiting for the child to exit, so the
// caller's cleanup runs and no tmux client is orphaned.
func runForwardingSignals(c *exec.Cmd) error {
	sigs, stop := attachSignalSource()
	defer stop()
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	for {
		select {
		case sig := <-sigs:
			// Fails harmlessly once the child has exited.
			_ = c.Process.Signal(sig)
		case err := <-done:
			return err
		}
	}
}

// childExitCode is the status leo exits with for a finished child: its own
// exit code, or 128+N when signal N killed it (ExitCode reports -1 then).
func childExitCode(e *exec.ExitError) int {
	if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return e.ExitCode()
}

// attachArgs is the `attach` subcommand argv for target under opts.
func attachArgs(target string, opts attachOptions) []string {
	args := []string{"attach"}
	if opts.readOnly {
		args = append(args, "-r")
	}
	return append(args, "-t", target)
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
		if opts.dispatchPlacement != "" {
			return fmt.Errorf("--dispatch-placement needs the remote leo to attach (leo attach or leo agent attach), not a raw tmux session")
		}
		if opts.cc {
			return attachRemoteControlMode(res, session)
		}
		// Bootstrap terminfo for the local $TERM on the remote so tmux there
		// doesn't bail with "missing or unsuitable terminal" (Ghostty, Kitty,
		// Alacritty, etc.). If install fails, we downgrade TERM on the remote
		// command so the attach still works.
		termOverride := ensureRemoteTerminfoFn(res)
		sshArgs := append([]string{"-t", res.Host.SSH}, res.Host.SSHArgs...)
		sshArgs = append(sshArgs, sshControlOpts(res)...)
		prefixLen := len(sshArgs)
		sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
		sshArgs = append(sshArgs, tmux.Args(attachArgs(remoteShellTarget(tmux.Target(session)), opts)...)...)
		sshArgs = applyRemoteTermFallback(sshArgs, prefixLen, termOverride)
		c := agentExecCommand("ssh", sshArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return hintRemoteTmuxMissing(res, c.Run())
	}

	if opts.dispatchPlacement != "" {
		if opts.asChild {
			return fmt.Errorf("--dispatch-placement cannot be used when the attach runs as a child process")
		}
		if tmuxEnv() != "" && !opts.cc {
			return fmt.Errorf("--dispatch-placement cannot be used from inside tmux (the attach opens as a popup, not a client of its own); detach first (prefix+d) and retry")
		}
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
		argv := append([]string{"tmux"}, tmux.Args(append([]string{"-CC"}, attachArgs(tmux.Target(session), opts)...)...)...)
		registerAttachPlacement(opts, session)
		return agentSyscallExec(tmuxPath, argv, utf8Locale(os.Environ()))
	}
	if tmuxEnv() != "" {
		inner := fmt.Sprintf("%s -L %s %s", shellQuoteArg(tmuxPath), tmux.SocketName, strings.Join(attachArgs(shellQuoteArg(tmux.Target(session)), opts), " "))
		popupArgs := []string{"display-popup", "-E", "-w", "95%", "-h", "95%", inner}
		c := agentExecCommand(tmuxPath, popupArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return c.Run()
	}
	if opts.asChild {
		c := agentExecCommand(tmuxPath, tmux.Args(attachArgs(tmux.Target(session), opts)...)...)
		c.Env = utf8Locale(os.Environ())
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		err := runForwardingSignals(c)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitCodeError{code: childExitCode(exitErr), err: errors.New("")}
		}
		return err
	}
	// Replace the CLI process so tmux owns the TTY cleanly. Returns an error
	// only if exec itself fails; on success this call does not return.
	argv := append([]string{"tmux"}, tmux.Args(attachArgs(tmux.Target(session), opts)...)...)
	registerAttachPlacement(opts, session)
	return agentSyscallExec(tmuxPath, argv, utf8Locale(os.Environ()))
}

// attachRemoteControlMode streams a remote agent's terminal over SSH using
// tmux control mode (`-CC`). The data plane needs three things the interactive
// remote-attach path does not:
//
//   - -tt : force a remote PTY. `tmux -CC attach` calls tcgetattr on its stdin
//     and aborts with "tcgetattr failed: Inappropriate ioctl for device" when
//     there is no terminal (verified empirically). -CC is *not* a no-TTY
//     protocol — iTerm2/WezTerm run it on a PTY too. tmux puts the remote PTY
//     into raw mode on attach, so the control-mode framing passes through
//     cleanly. -tt forces the PTY even when leo's own stdin is a pipe.
//   - -e none : disable SSH's own `~` escape character. Control-mode payloads
//     can begin a line with `~`, which ssh would otherwise intercept.
//   - shared ControlMaster : ride the `leo host forward` connection when it is
//     up (instant attach, no second auth); open our own otherwise.
//
// stdio is wired straight through so the caller owns the protocol stream. The
// caller (leoterm) is expected to drive this like a local `tmux -CC`: hand it a
// PTY (or pipe) and put its end in raw mode. We do not exec/replace the process
// — leoterm manages it as a child and tears it down by killing it.
func attachRemoteControlMode(res config.HostResolution, session string) error {
	sshArgs := []string{"-tt", "-e", "none", res.Host.SSH}
	sshArgs = append(sshArgs, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
	sshArgs = append(sshArgs, tmux.Args("-CC", "attach", "-t", remoteShellTarget(tmux.Target(session)))...)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return hintRemoteTmuxMissing(res, c.Run())
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9_.,/:@%+-]+$`)

// remoteShellWord quotes s for transit through a remote login shell, leaving
// words that need no quoting as they are. A leading "=" is among the quoted
// cases (see remoteShellTarget).
func remoteShellWord(s string) string {
	if plainShellWord.MatchString(s) {
		return s
	}
	return shellQuoteArg(s)
}

// utf8Locale returns env with an LC_CTYPE that tmux accepts as UTF-8: without
// one tmux replaces every non-ASCII byte it draws with "_". An env that
// already names a UTF-8 locale in LC_ALL, LC_CTYPE or LANG is returned as it
// is. A non-UTF-8 LC_CTYPE is replaced, not duplicated.
func utf8Locale(env []string) []string {
	isUTF8 := func(v string) bool {
		v = strings.ToLower(strings.ReplaceAll(v, "-", ""))
		return strings.Contains(v, "utf8")
	}
	for _, e := range env {
		for _, k := range []string{"LC_ALL=", "LC_CTYPE=", "LANG="} {
			if v, ok := strings.CutPrefix(e, k); ok && isUTF8(v) {
				return env
			}
		}
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "LC_CTYPE=") {
			out = append(out, e)
		}
	}
	return append(out, "LC_CTYPE=UTF-8")
}

// shellQuoteArg wraps a value in single quotes, escaping any embedded single
// quotes, so it can be safely embedded in a tmux display-popup command string.
// Paths and session names pass through `tmux display-popup -E "<cmd>"`, which
// hands the string to `/bin/sh -c`, so shell-quoting is required.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteShellTarget quotes a tmux `-t` target for transit through a remote
// login shell. ssh re-parses the whole command on the remote, so an exact-match
// target like "=leo-foo" (tmux.Target/PaneTarget, added for #87) is eaten by
// zsh's `=` filename expansion (the EQUALS option, on by default): zsh rewrites
// "=leo-foo" to the path of a command named "leo-foo" and aborts with
// "leo-foo not found" before tmux ever runs. Single-quoting passes the literal
// "=leo-foo" through untouched. Use this ONLY on ssh paths — local attaches hand
// argv straight to tmux with no shell in between, so a quoted target would reach
// tmux with the quotes still attached and fail to resolve.
func remoteShellTarget(target string) string { return shellQuoteArg(target) }

// followTmuxSession streams tmux pane output via `tail -f` on a pipe-pane log.
// Used by `leo agent logs -f`. When res is remote, it shells through ssh and
// uses the host's configured tmux path.
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
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, buildTailCmd(res.Host.RemoteTmuxPath()))
	return runShellCmd("ssh", sshArgs)
}

// attachViaDriver attaches to a driver-reported session. Every harness's
// AttachSpec is a tmux session (the TUI lives in the supervised pane), so
// this delegates to attachTmuxSession, which owns every attach flavor
// (nested-tmux popup, --cc control mode, terminfo fallback). Remote clients
// never reach here — `leo agent attach` delegates the whole command to the
// host-side leo (#104).
func attachViaDriver(res config.HostResolution, spec harness.AttachSpec, opts attachOptions) error {
	if spec.TmuxSession == "" {
		return fmt.Errorf("driver returned no attachable tmux session")
	}
	return attachTmuxSession(res, spec.TmuxSession, opts)
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
