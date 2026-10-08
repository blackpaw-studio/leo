package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

// watchSessionPrefix names the throwaway tmux sessions `dispatch attach`
// creates. It must never start with "leo-": that prefix marks supervised
// agents, and the daemon would try to manage it.
const watchSessionPrefix = consult.DispatchWatchSessionPrefix

// dispatchIDPattern is what a dispatch id may contain. It keeps an id usable
// as a tmux session name and as a single shell token on a remote host.
var dispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Testability seams: the daemon lookup and the watch-session suffix.
var (
	dispatchRecordFn = fetchDispatchRecord
	watchSuffixFn    = randomWatchSuffix
)

func fetchDispatchRecord(ctx context.Context, cfg *config.Config, id string) (consult.Record, error) {
	var record consult.Record
	err := dispatchHTTP(ctx, cfg, http.MethodGet, "/api/dispatch/"+url.PathEscape(id), nil, &record)
	return record, err
}

func randomWatchSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; the pid still
		// separates concurrent attaches.
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(b)
}

func newDispatchAttachCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach read-only to an interactive dispatch's TUI",
		Long: `Show an interactive dispatch's TUI, read-only, in this terminal.

The dispatch's tmux window is linked into a throwaway session and attached
with a read-only client, so nothing typed can reach the dispatch and no pane
is moved. Detaching (or the dispatch ending) removes the throwaway session;
the dispatch is untouched. Only a dispatch that has its own tmux window can
be attached: headless dispatches, ended ones, and panes split into their
caller's window are refused with exit status 1.

With --host, runs the remote leo over ssh -tt.`,
		Example: `  leo dispatch attach d-12ab34
  leo dispatch attach d-12ab34 --host prod`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !dispatchIDPattern.MatchString(id) {
				return fmt.Errorf("invalid dispatch id %q", id)
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				return runRemoteDispatchAttach(res, id)
			}
			return runDispatchAttachLocal(cmd.Context(), cfg, res, id)
		},
	}
	addHostFlag(cmd, &host)
	return cmd
}

// runRemoteDispatchAttach runs `leo dispatch attach <id>` on the remote host
// under a forced TTY. ssh joins its trailing arguments into one command line
// for the remote login shell, so the id travels as a single quoted token. The
// remote leo has already printed its one-line reason when it fails, so only
// its exit status is kept.
func runRemoteDispatchAttach(res config.HostResolution, id string) error {
	termOverride := ensureRemoteTerminfoFn(res)
	sshArgs := append([]string{"-tt", res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, sshControlOpts(res)...)
	prefixLen := len(sshArgs)
	sshArgs = append(sshArgs, res.Host.RemoteLeoPath(), "dispatch", "attach", shellQuoteArg(id))
	sshArgs = applyRemoteTermFallback(sshArgs, prefixLen, termOverride)
	c := agentExecCommand("ssh", sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = agentStdout
	c.Stderr = agentStderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitCodeError{code: exitErr.ExitCode(), err: errors.New("")}
	}
	return err
}

// runDispatchAttachLocal links dispatch id's window into a throwaway session
// and attaches to that read-only. The dispatch's pane is never moved: the
// window is linked (it then lives in both sessions) and the placeholder window
// that new-session created is killed.
func runDispatchAttachLocal(ctx context.Context, cfg *config.Config, res config.HostResolution, id string) error {
	if !dispatchIDPattern.MatchString(id) {
		return fmt.Errorf("invalid dispatch id %q", id)
	}
	rec, err := dispatchRecordFn(ctx, cfg, id)
	if err != nil {
		return fmt.Errorf("dispatch %s: %w", id, err)
	}
	if !consult.Attachable(rec) {
		return notAttachableError(rec)
	}
	tmuxPath, err := viewerLocateTmux()
	if err != nil {
		return err
	}
	tmuxOut := func(ctx context.Context, args ...string) (string, error) {
		out, err := viewerExecCommandContext(ctx, tmuxPath, tmux.Args(args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}

	// The record says the pane sits alone in its window; confirm that live
	// before linking, since a split since then would put the caller's own
	// panes in front of the watcher.
	probe, err := tmuxOut(ctx, "display-message", "-p", "-t", rec.PaneID, "#{window_id} #{window_panes}")
	if err != nil {
		return fmt.Errorf("dispatch %s: its tmux pane %s is gone", id, rec.PaneID)
	}
	window, panes, _ := strings.Cut(probe, " ")
	if window == "" || panes != "1" {
		return fmt.Errorf("dispatch %s shares its tmux window with other panes: not attachable", id)
	}

	name := watchSessionPrefix + id + "-" + watchSuffixFn()
	created, err := tmuxOut(ctx, "new-session", "-d", "-s", name, "-P", "-F", "#{session_id} #{window_id}")
	if err != nil {
		return fmt.Errorf("creating watch session: %w", err)
	}
	// Cleanup must survive the caller's cancellation, which is what usually
	// interrupts a failing attach.
	kill := func(target string) {
		_, _ = tmuxOut(context.WithoutCancel(ctx), "kill-session", "-t", target)
	}
	fields := strings.Fields(created)
	if len(fields) != 2 {
		kill(tmux.Target(name))
		return fmt.Errorf("creating watch session: unexpected tmux output %q", created)
	}
	sessionID, placeholder := fields[0], fields[1]
	// destroy-unattached destroys a session with no client at once, even one
	// that never had any, so setting it now would remove the session before
	// the attach below. A client-attached hook arms it once a client is on.
	setup := [][]string{
		{"link-window", "-d", "-s", window, "-t", tmux.Target(name) + ":"},
		{"kill-window", "-t", placeholder},
		{"set-option", "-t", sessionID, "status", "off"},
		{"set-hook", "-t", sessionID, "client-attached", "set-option destroy-unattached on"},
	}
	for _, args := range setup {
		if _, err := tmuxOut(ctx, args...); err != nil {
			kill(sessionID)
			return fmt.Errorf("preparing watch session (%s): %w", args[0], err)
		}
	}
	// The client runs as a child, not an exec, so this cleanup always runs:
	// a tmux that fails to start (no TTY) never arms destroy-unattached, and
	// a popup returns after the client left. Killing an already-destroyed
	// session fails harmlessly.
	err = attachTmuxSession(res, name, attachOptions{readOnly: true, asChild: true})
	kill(sessionID)
	return err
}

// notAttachableError says in one line why rec cannot be attached.
func notAttachableError(rec consult.Record) error {
	id := rec.ID
	switch {
	case rec.Mode != consult.ModeInteractive:
		return fmt.Errorf("dispatch %s is headless: nothing to attach (try `leo dispatch watch %s`)", id, id)
	case rec.Status.Terminal():
		return fmt.Errorf("dispatch %s has ended", id)
	case rec.Status == consult.StatusSettling:
		return fmt.Errorf("dispatch %s is settling; try again in a moment", id)
	case rec.PaneID == "":
		return fmt.Errorf("dispatch %s has no pane yet", id)
	case rec.ViewerKind == "split":
		return fmt.Errorf("dispatch %s runs in a split pane of its caller's window: not attachable", id)
	case rec.ViewerKind == "hidden":
		return fmt.Errorf("dispatch %s runs in a hidden pane: not attachable", id)
	}
	return fmt.Errorf("dispatch %s has no tmux window of its own: not attachable", id)
}
