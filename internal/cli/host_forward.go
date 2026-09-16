package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// remoteSockExpr resolves the remote daemon socket path under the remote shell:
// $LEO_HOME when set, else $HOME/.leo, matching leo's own home resolution.
// sshd does not expand env vars or ~ in a StreamLocalForward target, so we ask
// the remote shell to print the absolute path once at setup time.
const remoteSockExpr = `printf %s "${LEO_HOME:-$HOME/.leo}/state/leo.sock"`

// Forward tuning. Kept small and local — these are operational timers, not
// configuration the user is expected to touch. Vars (not consts) so tests can
// tighten them.
var (
	forwardConnectTimeout = 15 * time.Second
	forwardHealthPoll     = 250 * time.Millisecond
	forwardBackoffMin     = 1 * time.Second
	forwardBackoffMax     = 30 * time.Second
)

// hostForwarder owns the lifecycle of a single host's SSH socket forward.
type hostForwarder struct {
	res       config.HostResolution
	localSock string
	asJSON    bool
}

// forwardExecCommand builds the ssh process for one forward attempt. Seam so
// tests can stub ssh without a real host. Defaults to agentExecCommand so it
// shares the package-wide exec seam.
var forwardExecCommand = func(name string, args ...string) *exec.Cmd { return agentExecCommand(name, args...) }

// forwardHealthy reports whether the local forwarded socket answers /health.
// Seam so tests can drive the health transition deterministically.
var forwardHealthy = func(ctx context.Context, sockPath string) bool {
	return daemon.SocketHealthy(ctx, sockPath)
}

// run establishes and maintains the forward in the foreground. It returns nil
// on a clean shutdown (SIGINT/SIGTERM or ctx cancel after a healthy connect),
// and a non-zero error if the first connection never becomes healthy.
func (f *hostForwarder) run(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Idempotency: a healthy forward already serving this path means another
	// process owns it. Print the path and exit rather than stomping its socket
	// — and skip the remote round-trip entirely.
	if forwardHealthy(ctx, f.localSock) {
		f.printResult(0)
		return nil
	}

	remoteSock, err := f.remoteSockPath()
	if err != nil {
		return fmt.Errorf("resolving remote socket path on %s: %w", f.res.Name, err)
	}

	if err := os.MkdirAll(filepath.Dir(f.localSock), 0o700); err != nil {
		return fmt.Errorf("creating forward socket dir: %w", err)
	}

	backoff := forwardBackoffMin
	announced := false
	for {
		healthy, runErr := f.runOnce(ctx, remoteSock, func(pid int) {
			// Announce exactly once, on the first healthy connect. Reconnects
			// re-observe health but must not re-emit the socket path — the
			// contract is a single path line, and a consumer that has stopped
			// draining stdout would otherwise eventually block this loop.
			if announced {
				return
			}
			f.printResult(pid)
			announced = true
		})
		if healthy {
			backoff = forwardBackoffMin // good connection resets backoff
		}

		if ctx.Err() != nil {
			// Asked to stop (SIGINT/SIGTERM). The local socket is the remote
			// daemon's, removed by StreamLocalBindUnlink on the next bind; clean
			// it up anyway, along with the now-dead master's ControlPath file.
			f.removeForwardSockets()
			return nil
		}

		if !announced {
			// Never reached a healthy state — surface the failure so the
			// caller knows setup failed and no socket path was emitted.
			f.removeForwardSockets()
			return fmt.Errorf("ssh forward to %s failed before the socket came up: %w", f.res.Name, runErr)
		}

		fmt.Fprintf(agentStderr, "leo host forward %s: connection dropped (%v); reconnecting in %s\n",
			f.res.Name, runErrText(runErr), backoff)
		if !sleepCtx(ctx, backoff) {
			f.removeForwardSockets()
			return nil
		}
		backoff *= 2
		if backoff > forwardBackoffMax {
			backoff = forwardBackoffMax
		}
	}
}

// runOnce runs a single ssh forward process until it exits or ctx is cancelled.
// It polls the local socket for health; on the first healthy poll it invokes
// announce(pid). Returns whether health was ever observed and the ssh exit
// error (nil on a clean exit).
func (f *hostForwarder) runOnce(ctx context.Context, remoteSock string, announce func(pid int)) (bool, error) {
	cmd := f.sshForwardCmd(remoteSock)
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("starting ssh: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	poll := time.NewTicker(forwardHealthPoll)
	defer poll.Stop()
	// The connect deadline only guards the pre-healthy window: if the socket
	// never comes up we kill the attempt so the caller can fail fast or retry.
	deadline := time.NewTimer(forwardConnectTimeout)
	defer deadline.Stop()

	healthy := false
	for {
		select {
		case werr := <-done:
			return healthy, werr
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return healthy, ctx.Err()
		case <-poll.C:
			if !healthy && forwardHealthy(ctx, f.localSock) {
				healthy = true
				announce(cmd.Process.Pid)
			}
		case <-deadline.C:
			if !healthy {
				_ = cmd.Process.Kill()
				<-done
				return false, fmt.Errorf("forwarded socket did not become healthy within %s", forwardConnectTimeout)
			}
		}
	}
}

// sshForwardCmd builds the ssh StreamLocalForward command. It holds the
// ControlMaster so attach --cc and agent dispatches multiplex over this
// connection; ServerAlive probes surface dead links quickly so the reconnect
// loop can kick in; StreamLocalBindUnlink clears a stale local socket before
// binding; ExitOnForwardFailure makes a failed forward exit non-zero instead
// of silently sitting connected.
//
// No ControlPersist: this `-N` process *is* the master and keeps the
// connection alive for its whole lifetime, so persistence buys nothing — and
// it is actively harmful here. With ControlPersist, killing this process on a
// drop/reconnect forks a lingering background master holding the dead link;
// the respawn's ControlMaster=auto then re-attaches to that stale master and
// the forward never recovers. Without it, the kill cleanly ends the master and
// each reconnect establishes a fresh connection.
func (f *hostForwarder) sshForwardCmd(remoteSock string) *exec.Cmd {
	args := []string{
		"-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + f.res.ControlPath,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	args = append(args, f.res.Host.SSHArgs...)
	args = append(args, "-L", f.localSock+":"+remoteSock, f.res.Host.SSH)
	return forwardExecCommand("ssh", args...)
}

// remoteSockPath asks the remote shell for the absolute daemon socket path.
//
// ssh space-joins every post-destination argv token into a single string and
// hands that to the remote login shell — it does not preserve our argv
// boundaries. So `sh -c <expr>` as three tokens arrives as `sh -c printf %s
// "..."`, where `sh -c` takes only the first word (`printf`) as the command and
// the rest become unused positional args, yielding a printf usage error. We
// single-quote the expr into one token so the flattened string is `sh -c
// '<expr>'` and the whole expr reaches `sh -c` intact.
func (f *hostForwarder) remoteSockPath() (string, error) {
	args := buildSSHArgs(f.res, "sh", "-c", shellQuoteArg(remoteSockExpr))
	cmd := agentExecCommand("ssh", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	path := strings.TrimSpace(out.String())
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("remote returned a non-absolute socket path %q", path)
	}
	return path, nil
}

// stop tears down a lingering ControlMaster and removes the stale local socket.
// Best-effort: a missing master or socket is not an error.
func (f *hostForwarder) stop() error {
	// `ssh -O exit` asks the master at ControlPath to terminate. Options must
	// precede the destination, which goes last.
	exitArgs := []string{
		"-o", "ControlPath=" + f.res.ControlPath,
		"-O", "exit",
	}
	exitArgs = append(exitArgs, f.res.Host.SSHArgs...)
	exitArgs = append(exitArgs, f.res.Host.SSH)
	cmd := agentExecCommand("ssh", exitArgs...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	_ = cmd.Run() // no live master is fine

	f.removeForwardSockets()
	fmt.Fprintf(agentStdout, "tore down forward for %s\n", f.res.Name)
	return nil
}

// removeForwardSockets unlinks both the forwarded local socket and the
// ControlMaster socket file. The `-N` forward process IS the master (no
// ControlPersist), so once it's gone — whether via `ssh -O exit` (unforward) or
// a SIGTERM that kills this process and its ssh child — the ControlPath file is
// stale and should be cleaned up. Without this, a plain SIGTERM teardown leaves
// a dangling <home>/state/remotes/<host>.ctl. Best-effort: missing files are
// fine, and StreamLocalBindUnlink would clear the local socket on a re-bind
// anyway.
func (f *hostForwarder) removeForwardSockets() {
	_ = os.Remove(f.localSock)
	if f.res.ControlPath != "" {
		_ = os.Remove(f.res.ControlPath)
	}
}

// printResult emits the local socket path (plain or JSON). The plain form is a
// single parseable line; the JSON form carries the host and the ssh pid (0 when
// an existing healthy forward was reused).
func (f *hostForwarder) printResult(pid int) {
	if f.asJSON {
		enc := json.NewEncoder(agentStdout)
		_ = enc.Encode(struct {
			Socket string `json:"socket"`
			Host   string `json:"host"`
			PID    int    `json:"pid"`
		}{Socket: f.localSock, Host: f.res.Name, PID: pid})
		return
	}
	fmt.Fprintln(agentStdout, f.localSock)
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true if the full
// duration elapsed, false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runErrText renders an ssh exit error for the reconnect log, collapsing the
// common "signal: killed" / nil cases to something readable.
func runErrText(err error) string {
	if err == nil {
		return "connection closed"
	}
	return err.Error()
}
