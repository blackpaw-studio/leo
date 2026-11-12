package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/remote"
	"github.com/spf13/cobra"
)

// fallbackShell is the shell of last resort when neither $SHELL nor the
// passwd entry names a usable one.
const fallbackShell = "/bin/sh"

// Testability seams for `leo agent shell`.
var (
	agentChdir  = os.Chdir
	userShellFn = passwdShell
)

func newAgentShellCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "shell [--] <name>",
		Short: "Open a login shell in the agent's workspace",
		Long: `Open an interactive login shell ($SHELL -l, else the account's passwd shell,
else /bin/sh) on the machine the agent lives on, with its working directory set
to the agent's workspace. The shell's exit status is this command's exit status.

Locally the shell replaces this process. With --host the command runs
'ssh -tt <host> <leo_path> agent shell --host localhost -- <name>', the same transport as
'agent attach', so the remote leo does the resolution and the exec.

An unknown agent, an agent that moved to another host, or a workspace that is
missing or not a directory is refused with a nonzero exit.`,
		Example: `  # Shell in an agent's workspace
  leo agent shell fetch

  # On a remote host from client.hosts
  leo --host prod agent shell -- fetch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemoteShell(res, name)
			}
			dir, err := resolveShellWorkspace(cmd.Context(), cfg.HomePath, name)
			if err != nil {
				return err
			}
			return execLoginShell(dir)
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// resolveShellWorkspace resolves query through the daemon and returns the
// agent's workspace, refusing an unknown or moved agent and a workspace that is
// unset, missing or not a directory.
func resolveShellWorkspace(ctx context.Context, homePath, query string) (string, error) {
	session, err := lookupAgentSession(ctx, homePath, query)
	var notFound *agent.ErrNotFound
	switch {
	case errors.As(err, &notFound):
		return "", fmt.Errorf("no agent named %q", query)
	case err != nil:
		return "", err
	case session.Name == "":
		return "", fmt.Errorf("no agent named %q", query)
	}
	if session, err = awaitRestoredSession(ctx, homePath, query, session); err != nil {
		return "", err
	}
	if err := movedSessionError(session); err != nil {
		return "", err
	}

	records, err := agentListFn(ctx, homePath)
	if err != nil {
		return "", fmt.Errorf("listing agents: %w", err)
	}
	workspace := ""
	for _, r := range records {
		if r.Name == session.Name {
			workspace = r.Workspace
			break
		}
	}
	if workspace == "" {
		return "", fmt.Errorf("agent %q has no workspace to open a shell in", session.Name)
	}
	// A relative path would resolve against wherever this process (or an ssh
	// login) happens to be, not against the agent's own tree.
	if !filepath.IsAbs(workspace) {
		return "", fmt.Errorf("workspace %q of agent %q is not an absolute path", workspace, session.Name)
	}
	info, err := os.Stat(workspace)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("workspace %s of agent %q does not exist", workspace, session.Name)
	case err != nil:
		return "", fmt.Errorf("checking workspace %s of agent %q: %w", workspace, session.Name, err)
	case !info.IsDir():
		return "", fmt.Errorf("workspace %s of agent %q is not a directory", workspace, session.Name)
	}
	return workspace, nil
}

// execLoginShell replaces this process with a login shell in dir, so the
// shell owns the TTY and its exit status is ours.
func execLoginShell(dir string) error {
	shell := selectShell(os.Getenv("SHELL"), userShellFn)
	if err := agentChdir(dir); err != nil {
		return fmt.Errorf("entering workspace %s: %w", dir, err)
	}
	return agentSyscallExec(shell, []string{shell, "-l"}, utf8Locale(shellEnv(os.Environ(), dir)))
}

// runRemoteShell runs `leo agent shell -- <name>` on res's host over a forced
// PTY. Every token after the host is shell-quoted because ssh flattens them
// into one string for the remote login shell. --host localhost makes the
// remote leo run the shell on itself, ignoring its own client.default_host,
// LEO_HOST and peers, so the shell never hops to a third machine.
func runRemoteShell(res config.HostResolution, name string) error {
	// The remote shell inherits $TERM through ssh; bootstrap its terminfo, or
	// downgrade TERM on the remote command, as attach does.
	termOverride := ensureRemoteTerminfoFn(res)
	sshArgs := append([]string{"-tt"}, remote.SSHArgv(res, false)...)
	prefixLen := len(sshArgs)
	sshArgs = append(sshArgs, remoteLeoWord(res.Host.RemoteLeoPath()), "agent", "shell", "--host", config.LocalhostSentinel, "--", remote.Quote(name))
	sshArgs = applyRemoteTermFallback(sshArgs, prefixLen, termOverride)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	err := runForwardingSignals(c)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// The remote shell (or ssh) already said what it had to say.
		return exitCodeError{code: childExitCode(exitErr), err: errors.New("")}
	}
	return err
}

// selectShell picks the login shell: $SHELL, else the passwd shell, else
// /bin/sh. A candidate must be an absolute path, since exec does no PATH
// lookup. passwd is consulted only when $SHELL is unusable.
func selectShell(envShell string, passwd func() string) string {
	if filepath.IsAbs(envShell) {
		return envShell
	}
	if s := passwd(); filepath.IsAbs(s) {
		return s
	}
	return fallbackShell
}

// shellEnv is env as the agent shell should see it: no TMUX or TMUX_PANE (the
// shell is not inside the caller's tmux, matching agent panes) and PWD pinned
// to dir.
func shellEnv(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "TMUX="), strings.HasPrefix(kv, "TMUX_PANE="), strings.HasPrefix(kv, "PWD="):
		default:
			out = append(out, kv)
		}
	}
	return append(out, "PWD="+dir)
}

// passwdShell is the current account's login shell from the system user
// database, or "" when it cannot be read.
func passwdShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
		if err != nil {
			return ""
		}
		return shellFromDscl(string(out))
	}
	if out, err := exec.Command("getent", "passwd", u.Username).Output(); err == nil {
		if s := shellFromPasswdLine(string(out)); s != "" {
			return s
		}
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if strings.HasPrefix(string(line), u.Username+":") {
			return shellFromPasswdLine(string(line))
		}
	}
	return ""
}

// shellFromPasswdLine is the shell (seventh field) of one passwd entry.
func shellFromPasswdLine(line string) string {
	fields := strings.Split(strings.TrimSpace(line), ":")
	if len(fields) < 7 {
		return ""
	}
	return fields[6]
}

// shellFromDscl parses `dscl . -read /Users/<u> UserShell` output.
func shellFromDscl(out string) string {
	s, ok := strings.CutPrefix(strings.TrimSpace(out), "UserShell:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
