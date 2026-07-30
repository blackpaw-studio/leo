// Server-lifecycle helpers for leo's dedicated tmux socket.
//
// On macOS, `tmux new-session -d` auto-spawns a tmux SERVER the first time
// it's needed. That server double-forks and reparents to launchd (PID 1),
// which severs the "responsible process" chain the OS uses to grant Local
// Network access — so third-party binaries running under agent sessions
// (node, claude's HTTP MCP clients) get silently denied access to LAN hosts.
//
// The fix is for the leo daemon to own a long-lived FOREGROUND tmux server
// (`tmux -L leo -D`), started as a direct child of the signed leo binary, so
// macOS stamps responsibility on leo at server-creation time. Every agent
// session created afterward — as a client on that same socket — inherits the
// grant. These helpers implement that lifecycle: probe whether a server is
// already up, tell a leo-started foreground server apart from a legacy
// auto-daemonized one, recycle the legacy case once, and keep the foreground
// server itself alive independent of the daemon's own lifetime (it must
// survive daemon SIGKILL — see EnsureForegroundServer and
// StartForegroundServer).
package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// serverExecCommand is the seam tests replace for the short-lived
// server-lifecycle probes (display-message, show-options, set, kill-server).
// Deliberately not context-bound — these calls are used both from the
// daemon's own startup path and from the detached supervise loop, neither of
// which has a request-scoped context to hang them off.
var serverExecCommand = exec.Command

// startForegroundCommand is the seam tests replace for launching the
// long-lived foreground server itself. Kept separate from serverExecCommand
// so tests can stub the quick probes without having to also fake a
// real backgrounded, self-sustaining process.
var startForegroundCommand = exec.Command

// foregroundMarkerKey is the tmux global option leo sets on a foreground
// server it started, so a later EnsureForegroundServer call (e.g. after a
// daemon restart that adopts a still-running server) can tell it apart from
// a legacy auto-daemonized one.
const foregroundMarkerKey = "@leo-foreground"

// foregroundStartAttempts/foregroundStartPoll bound how long
// StartForegroundServer waits for the freshly launched server to start
// answering before giving up: 50 * 100ms = 5s. Package-level vars (not
// consts) so tests can shrink them to keep the timeout/restart-loop tests
// fast.
var (
	foregroundStartAttempts = 50
	foregroundStartPoll     = 100 * time.Millisecond
)

// foregroundSuperviseInterval controls how often SuperviseForegroundServer
// polls for the foreground server having exited. A var for the same reason.
var foregroundSuperviseInterval = 2 * time.Second

// ServerRunning reports whether a tmux server is currently listening on
// leo's dedicated socket.
func ServerRunning(tmuxPath string) bool {
	return serverExecCommand(tmuxPath, Args("display-message", "-p", "#{pid}")...).Run() == nil
}

// markerState reports whether the running server carries leo's foreground
// marker. ok=false means the probe itself failed (server didn't answer
// cleanly) — callers MUST NOT treat that as "unmarked", because the
// destructive recycle path keys off "unmarked".
//
// Deliberately probes via `show-options -g` (the full global-option dump)
// rather than `show-options -gv @leo-foreground`: the latter exits 1 with
// "invalid option" whenever the option is simply UNSET, which is
// indistinguishable from a transient probe failure and would misclassify a
// momentary hiccup on leo's OWN (marked) server as "legacy → recycle" —
// destroying every live agent session. The full dump exits 0 on any live
// server regardless of whether the marker is present.
// Key matching is delegated to optionValue, which anchors on the whole option
// name. That matters here: @leo-foreground-owner (ownership.go) is a
// superstring of this key, so a prefix match would let an owner stamp alone
// report the server as marked — adopting a legacy daemonized server instead of
// recycling it, which is the exact silent attribution loss this marker exists
// to prevent.
func markerState(tmuxPath string) (marked bool, ok bool) {
	out, err := serverExecCommand(tmuxPath, Args("show-options", "-g")...).Output()
	if err != nil {
		return false, false
	}
	_, found := optionValue(string(out), foregroundMarkerKey)
	return found, true
}

// markForeground stamps the currently running server with leo's foreground
// marker.
func markForeground(tmuxPath string) error {
	out, err := serverExecCommand(tmuxPath, Args("set", "-g", foregroundMarkerKey, "1")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mark tmux server as foreground: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// KillServer terminates the tmux server on leo's socket, along with every
// session it hosts. Used once to recycle a legacy auto-daemonized server
// before EnsureForegroundServer starts a fresh foreground one in its place.
func KillServer(tmuxPath string) error {
	out, err := serverExecCommand(tmuxPath, Args("kill-server")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kill tmux server: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StartForegroundServer launches a long-lived tmux server in foreground mode
// (`tmux -L leo -D`, run with no subcommand) as a direct child of the
// calling process, waits for it to start answering, stamps it with leo's
// foreground marker, then releases it from Go's process-tracking so it can
// reparent cleanly to launchd once the caller exits.
//
// The server is intentionally NOT tied to any context: Setpgid puts it in
// its own process group so a signal delivered to the daemon's group (e.g.
// `leo update`'s SIGKILL) doesn't also kill it, and Release() detaches the
// os.Process so nothing in this process waits on or reaps it. It must
// outlive the daemon — agent sessions created on its socket depend on that
// for the daemon-restart adoption behavior the supervisor already relies on.
func StartForegroundServer(tmuxPath string) (*os.Process, error) {
	cmd := startForegroundCommand(tmuxPath, Args("-D")...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start foreground tmux server: %w", err)
	}

	if !waitForServer(tmuxPath) {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("foreground tmux server did not come up within %s",
			time.Duration(foregroundStartAttempts)*foregroundStartPoll)
	}

	if err := markForeground(tmuxPath); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	// Record which leo process created this server, so a later adoption can
	// report whether the process macOS attributed Local Network
	// responsibility to is still alive (see ownership.go). Deliberately
	// fail-open: the stamp is diagnostic metadata, and killing a working
	// server over a missing diagnostic would be worse than the condition it
	// helps diagnose.
	if err := stampOwner(tmuxPath, os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "tmux: could not record server ownership: %v\n", err)
	}

	// Release() detaches the process from Go's tracking without Wait()ing on
	// it, by design: the server must outlive this process (see the doc
	// comment above). The accepted tradeoff is that if the server later
	// crashes, that child becomes a zombie until this daemon process itself
	// exits and init/launchd reaps it — bounded to at most one zombie per
	// crash, not an accumulating leak, so it's not worth a manual Wait4 here.
	proc := cmd.Process
	pid := proc.Pid
	if err := proc.Release(); err != nil {
		return nil, fmt.Errorf("release foreground tmux server process: %w", err)
	}
	// On Unix, Release() zeroes Pid as part of marking the handle unusable
	// for future Wait/Kill/Signal calls. Callers here only want Pid for
	// logging (see EnsureForegroundServer), never process control post
	// release, so restore it for that purpose.
	proc.Pid = pid
	return proc, nil
}

// waitForServer polls ServerRunning up to foregroundStartAttempts times,
// foregroundStartPoll apart.
func waitForServer(tmuxPath string) bool {
	for i := 0; i < foregroundStartAttempts; i++ {
		if ServerRunning(tmuxPath) {
			return true
		}
		time.Sleep(foregroundStartPoll)
	}
	return false
}

// waitForServerGone polls ServerRunning after a kill-server, up to
// foregroundStartAttempts times foregroundStartPoll apart, so the
// subsequent StartForegroundServer call doesn't race the dying legacy
// server for leo's socket. Best-effort: it returns once the server is gone
// or the poll budget is exhausted either way, since StartForegroundServer's
// own readiness poll (waitForServer) is the backstop if the socket is still
// momentarily contested.
func waitForServerGone(tmuxPath string) {
	for i := 0; i < foregroundStartAttempts; i++ {
		if !ServerRunning(tmuxPath) {
			return
		}
		time.Sleep(foregroundStartPoll)
	}
}

// EnsureForegroundServer makes sure leo's dedicated tmux socket is backed by
// a foreground server leo itself started, so every session created on it —
// including ones spawned later by the supervise loops — inherits macOS Local
// Network responsibility from the leo binary instead of an auto-daemonized,
// PID-1-reparented tmux server.
//
// Must be called once at daemon startup, before RestoreAgents or any
// supervise loop issues a `new-session`. Three outcomes:
//   - A foreground server (leo's own, from a prior run) is already up: adopt
//     it, no-op.
//   - A server is up but unmarked (a legacy auto-daemonized one, or one from
//     before this feature existed): recycle it once — kill it and start a
//     fresh foreground one. Every agent session hosted on it is lost and
//     restarts via the normal supervise-loop recovery path.
//   - No server is up: start a fresh foreground one.
//
// Callers should treat a non-nil error as fail-open: log a warning and keep
// starting the daemon rather than aborting it. Falling back to tmux's own
// auto-daemonized server (today's behavior) is worse than a dead daemon, but
// strictly better than refusing to start at all over this optimization.
func EnsureForegroundServer(tmuxPath string) error {
	if ServerRunning(tmuxPath) {
		marked, ok := markerState(tmuxPath)
		if marked {
			fmt.Fprintln(os.Stdout, "tmux: adopting existing foreground server")
			return nil
		}
		if !ok {
			// The marker probe itself was inconclusive (the server didn't
			// answer cleanly). Do NOT recycle a server we can't confirm is
			// unmarked — that would risk bouncing every live agent session
			// over a transient hiccup. Adopt it if it's still actually up;
			// otherwise fall through to a fresh start below.
			if ServerRunning(tmuxPath) {
				fmt.Fprintln(os.Stdout, "tmux: adopting existing tmux server (marker probe inconclusive)")
				return nil
			}
		} else {
			// The server answered cleanly and is genuinely unmarked: a
			// legacy auto-daemonized server. Safe to recycle once.
			fmt.Fprintln(os.Stderr, "tmux: recycling legacy daemonized tmux server to enable macOS Local Network attribution; agent sessions will restart once")
			if err := KillServer(tmuxPath); err != nil {
				return fmt.Errorf("recycle legacy tmux server: %w", err)
			}
			waitForServerGone(tmuxPath)
		}
	}

	proc, err := StartForegroundServer(tmuxPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "tmux: started foreground server (pid %d)\n", proc.Pid)
	return nil
}

// SuperviseForegroundServer polls leo's tmux socket and restarts the
// foreground server if it ever exits unexpectedly, giving it
// restart-on-crash behavior without tying its lifetime to ctx: cancelling
// ctx stops this monitor loop but deliberately leaves a still-running server
// alone (see StartForegroundServer's Setpgid + Release lifetime contract —
// it must survive daemon shutdown, clean or SIGKILL).
func SuperviseForegroundServer(ctx context.Context, tmuxPath string) {
	ticker := time.NewTicker(foregroundSuperviseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ServerRunning(tmuxPath) {
			continue
		}
		fmt.Fprintln(os.Stderr, "tmux: foreground server exited; restarting")
		if _, err := StartForegroundServer(tmuxPath); err != nil {
			fmt.Fprintf(os.Stderr, "tmux: failed to restart foreground server: %v\n", err)
		}
	}
}
