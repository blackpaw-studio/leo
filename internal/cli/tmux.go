package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
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
		sshArgs = append(sshArgs, tmux.Args("attach", "-t", remoteShellTarget(tmux.Target(session)))...)
		sshArgs = applyRemoteTermFallback(sshArgs, prefixLen, termOverride)
		c := agentExecCommand("ssh", sshArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return hintRemoteTmuxMissing(res, c.Run())
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
		argv := append([]string{"tmux"}, tmux.Args("-CC", "attach", "-t", tmux.Target(session))...)
		return agentSyscallExec(tmuxPath, argv, os.Environ())
	}
	if tmuxEnv() != "" {
		inner := fmt.Sprintf("%s -L %s attach -t %s", shellQuoteArg(tmuxPath), tmux.SocketName, shellQuoteArg(tmux.Target(session)))
		popupArgs := []string{"display-popup", "-E", "-w", "95%", "-h", "95%", inner}
		c := agentExecCommand(tmuxPath, popupArgs...)
		c.Stdin = os.Stdin
		c.Stdout = agentStdout
		c.Stderr = agentStderr
		return c.Run()
	}
	// Replace the CLI process so tmux owns the TTY cleanly. Returns an error
	// only if exec itself fails; on success this call does not return.
	argv := append([]string{"tmux"}, tmux.Args("attach", "-t", tmux.Target(session))...)
	return agentSyscallExec(tmuxPath, argv, os.Environ())
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

// captureTmuxPane runs a one-shot `tmux capture-pane -p -S -<lines>` against
// the given session and writes output to the shared agentStdout. Local and
// remote paths share identical shape — remote just wraps through ssh with the
// host's configured tmux path.
func captureTmuxPane(res config.HostResolution, session string, lines int) error {
	tail := []string{"-p", "-S", fmt.Sprintf("-%d", lines)}
	if res.Localhost {
		tmuxPath, err := tmuxLocate()
		if err != nil {
			return err
		}
		// Local: argv goes straight to tmux, no shell, so the raw "=name:" target
		// resolves as-is.
		subArgs := tmux.Args(append([]string{"capture-pane", "-t", tmux.PaneTarget(session)}, tail...)...)
		return runShellCmd(tmuxPath, subArgs)
	}
	// Remote: the target crosses the remote login shell, so quote the exact-match
	// pane target to survive zsh `=` expansion (see remoteShellTarget).
	subArgs := tmux.Args(append([]string{"capture-pane", "-t", remoteShellTarget(tmux.PaneTarget(session))}, tail...)...)
	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
	sshArgs = append(sshArgs, subArgs...)
	return hintRemoteTmuxMissing(res, runShellCmd("ssh", sshArgs))
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
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, buildTailCmd(res.Host.RemoteTmuxPath()))
	return runShellCmd("ssh", sshArgs)
}

// attachHistoryTailLines bounds how much of a non-live-attach harness's turn
// history is printed when AttachSpec.Argv is nil (see attachViaDriver).
const attachHistoryTailLines = 50

// leoTUIWindowKeyOption is the tmux window user option storing the WindowKey
// a tmux-flavored AttachSpec's window was last created with. Comparing it on
// the next attach tells ensureTmuxWindow "reuse" from "the harness session id
// rotated, recreate" — chosen over #{pane_start_command} after live testing
// showed that field unreliable for staleness detection.
const leoTUIWindowKeyOption = "@leo_tui_key"

// windowTarget composes an exact-match tmux target-window ("=session:window")
// from a session name and window name, extending tmux.Target/PaneTarget's
// exact-match rationale (avoiding tmux's prefix-matching fallback) to
// window-qualified targets.
func windowTarget(session, window string) string {
	return tmux.Target(session) + ":" + window
}

// attachViaDriver carries out a harness.AttachSpec resolved by a
// SessionDriver — the non-claude counterpart to attachTmuxSession, which
// handles claude targets. Three shapes, checked in order:
//
//   - spec.TmuxSession != "": tmux-flavored attach (opencode today). Ensure
//     the window (see ensureTmuxWindow), select it, then delegate to
//     attachTmuxSession so every existing flavor (nested-tmux popup, --cc
//     control mode, remote ssh + terminfo handling) applies unchanged. This
//     always runs on the host: remote dispatch of `leo agent attach` /
//     `leo attach` is already delegated to the remote leo binary before this
//     function is ever reached with a tmux-flavored spec (see #104), so no
//     remote branch is needed here.
//   - len(spec.Argv) != 0: exec directly — locally via agentSyscallExec
//     (replacing this process, same as attachTmuxSession's outside-tmux
//     branch), or over `ssh -tt -e none` with every token shell-quoted for a
//     remote host (ssh flattens post-host argv into one shell string on the
//     remote login shell — quoting each token individually keeps them
//     intact).
//   - otherwise: no live attach; the tail of spec.HistoryPath is printed
//     instead, with a one-line note.
//
// The daemon's AttachSpec always sends a bare binary name at argv[0] (e.g.
// "opencode") — syscall.Exec needs a real path, not a PATH-relative name, so
// the local branch resolves it via exec.LookPath first. argv itself (which
// becomes the exec'd program's os.Args) keeps the original bare name at
// argv[0]; only the exec path is resolved. The remote branch leaves argv
// untouched — the remote login shell resolves it against its own PATH.
func attachViaDriver(res config.HostResolution, spec harness.AttachSpec, opts attachOptions) error {
	if spec.TmuxSession != "" {
		return attachViaDriverTmux(res, spec, opts)
	}
	if len(spec.Argv) == 0 {
		return printAttachHistory(spec.HistoryPath)
	}
	if res.Localhost {
		resolved, err := exec.LookPath(spec.Argv[0])
		if err != nil {
			return fmt.Errorf("%s not found on PATH", spec.Argv[0])
		}
		return agentSyscallExec(resolved, spec.Argv, os.Environ())
	}
	quoted := make([]string, len(spec.Argv))
	for i, tok := range spec.Argv {
		quoted[i] = shellQuoteArg(tok)
	}
	sshArgs := []string{"-tt", "-e", "none", res.Host.SSH}
	sshArgs = append(sshArgs, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	sshArgs = append(sshArgs, strings.Join(quoted, " "))
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	return hintRemoteTmuxMissing(res, c.Run())
}

// attachViaDriverTmux ensures a tmux-flavored AttachSpec's window exists,
// selects it, then hands off to attachTmuxSession for the rest of the attach
// (nested-tmux popup, --cc, terminfo). Localhost only — see attachViaDriver's
// doc for why the remote case never reaches here.
func attachViaDriverTmux(res config.HostResolution, spec harness.AttachSpec, opts attachOptions) error {
	tmuxPath, err := tmuxLocate()
	if err != nil {
		return err
	}
	if err := ensureTmuxWindow(tmuxPath, spec.TmuxSession, spec.WindowName, spec.WindowCmd, spec.WindowKey); err != nil {
		return err
	}
	if err := runShellCmd(tmuxPath, tmux.Args("select-window", "-t", windowTarget(spec.TmuxSession, spec.WindowName))); err != nil {
		return fmt.Errorf("selecting %s window: %w", spec.WindowName, err)
	}
	return attachTmuxSession(res, spec.TmuxSession, opts)
}

// tmuxWindowKey reports the leoTUIWindowKeyOption stored on session's window
// named windowName, and whether that window exists at all. Uses
// list-windows' -F format expansion (not show-options) so an existing window
// whose option was never set reports exists=true, key="" instead of being
// indistinguishable from a missing window — show-options errors on both a
// missing target AND an unset user option, which would conflate them.
// list-windows failing outright (e.g. no such tmux session yet) is reported
// as exists=false; the caller's create path surfaces any real problem when
// it tries to create the window.
func tmuxWindowKey(tmuxPath, session, windowName string) (key string, exists bool) {
	out, err := agentExecCommand(tmuxPath, tmux.Args("list-windows", "-t", tmux.Target(session), "-F", "#{window_name}\t#{"+leoTUIWindowKeyOption+"}")...).Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name, k, ok := strings.Cut(line, "\t")
		if ok && name == windowName {
			return k, true
		}
	}
	return "", false
}

// ensureTmuxWindow makes sure session has a window named windowName running
// cmd, recreating it if a prior window's stored key doesn't match key. A
// matching existing window is left completely alone (no kill, no
// recreation) — reusing the live TUI process is the whole point of the key
// check.
func ensureTmuxWindow(tmuxPath, session, windowName string, cmd []string, key string) error {
	existingKey, exists := tmuxWindowKey(tmuxPath, session, windowName)
	if exists && existingKey == key {
		return nil
	}
	if exists {
		if err := runShellCmd(tmuxPath, tmux.Args("kill-window", "-t", windowTarget(session, windowName))); err != nil {
			return fmt.Errorf("killing stale %s window: %w", windowName, err)
		}
	}
	quoted := make([]string, len(cmd))
	for i, tok := range cmd {
		quoted[i] = shellQuoteArg(tok)
	}
	windowCmd := strings.Join(quoted, " ")
	if err := runShellCmd(tmuxPath, tmux.Args("new-window", "-d", "-t", tmux.Target(session), "-n", windowName, windowCmd)); err != nil {
		return fmt.Errorf("creating %s window: %w", windowName, err)
	}
	if err := runShellCmd(tmuxPath, tmux.Args("set-option", "-w", "-t", windowTarget(session, windowName), leoTUIWindowKeyOption, key)); err != nil {
		return fmt.Errorf("tagging %s window: %w", windowName, err)
	}
	return nil
}

// printAttachHistory writes a "no live attach" note followed by the tail of
// path (last attachHistoryTailLines lines, or the whole file if shorter) to
// agentStdout. An empty path or a read failure still returns nil — a missing
// history file is not a fatal attach error, just nothing to show yet.
func printAttachHistory(path string) error {
	fmt.Fprintln(agentStdout, "note: this harness has no live attach; showing recent turn history:")
	if path == "" {
		fmt.Fprintln(agentStdout, "(no history file available yet)")
		return nil
	}
	f, err := os.Open(path) // #nosec G304 -- path comes from the resolved driver's own AttachSpec, not user input
	if err != nil {
		fmt.Fprintf(agentStdout, "(could not read history: %v)\n", err)
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > attachHistoryTailLines {
			lines = lines[1:]
		}
	}
	for _, line := range lines {
		fmt.Fprintln(agentStdout, line)
	}
	return nil
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
