package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

// Testability seams — overridden in tests. agentStartFn wraps daemon.AgentStart
// so tests can assert it fires (or doesn't) without a real daemon socket.
// agentSessionReadyFn wraps the tmux has-session probe used to wait out the
// gap between AgentStart returning and the respawned tmux session actually
// existing (SpawnAgent hands off to an async supervise goroutine — see
// internal/service/process.go — so the daemon call itself is not proof the
// session is there yet).
var (
	agentStartFn        = daemon.AgentStart
	agentSessionReadyFn = defaultAgentSessionReady
)

// agentStartPollInterval/agentStartTimeout are vars, not consts, so tests can
// shrink them to exercise the timeout path without a real multi-second wait.
var (
	// agentStartPollInterval is how often ensureAgentRunning re-checks
	// whether the respawned agent's tmux session has appeared.
	agentStartPollInterval = 200 * time.Millisecond
	// agentStartTimeout bounds the wait so a wedged respawn (crashed claude
	// binary, tmux server hung, etc.) fails with a clear error instead of
	// hanging the attach command forever.
	agentStartTimeout = 10 * time.Second
)

// defaultAgentSessionReady reports whether name's tmux session currently
// exists. Errors locating tmux are treated as "not ready" — the caller's
// bounded poll surfaces a clear timeout rather than this function returning
// a locate error that would be confusing mid-wait.
func defaultAgentSessionReady(name string) bool {
	tmuxPath, err := tmuxLocate()
	if err != nil {
		return false
	}
	session := agentSessionName(name)
	cmd := agentExecCommand(tmuxPath, tmux.Args("has-session", "-t", tmux.Target(session))...)
	return cmd.Run() == nil
}

// ensureAgentRunning is the shared decision point both cobra-command attach
// doors (`leo attach` and `leo agent attach`) call after resolving a name to
// a session. stopped comes from the daemon's AgentSessionResponse — already
// paid for by the caller's lookup — so this never re-resolves the agent
// itself.
//
// A live agent (stopped == false) is a no-op: returns (true, nil)
// immediately so neither the gate nor the prompt is ever consulted on the
// hot path.
//
// A dormant agent is gated FIRST — before any prompt — so a user whose
// leo_stop_agent is denied is refused immediately instead of being walked
// through a Y/n prompt only to be refused after answering. It then prompts
// on a TTY (mirroring resolveSpawnCollision's cancel-on-decline convention:
// declining is not an error, it's a quiet return to the shell) or fails fast
// with the exact command to run when stdin isn't interactive, since blocking
// on an unanswerable prompt would just hang a script.
//
// Returns (false, nil) when the user declined — callers must treat that as
// "stop here, no error" rather than short-circuiting into an attach.
//
// The gateCommand call below is required, not just documentation:
// TestEverySpawnRouteIsGated (permissions_test.go) statically scans every
// source file that mentions daemon.AgentStart (via agentStartFn above) for
// the literal substring gateCommand(cmd, "leo_stop_agent").
func ensureAgentRunning(ctx context.Context, cmd *cobra.Command, homePath, name string, stopped bool) (bool, error) {
	if !stopped {
		return true, nil
	}

	if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
		return false, err
	}

	if !agentIsTTY() {
		return false, fmt.Errorf("agent %q is stopped; run: leo agent start %s", name, name)
	}

	if _, err := fmt.Fprintf(agentStderr, "agent %q is stopped. Start it? [Y/n] ", name); err != nil {
		return false, fmt.Errorf("writing prompt: %w", err)
	}
	reader := bufio.NewReader(agentStdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading choice: %w", err)
	}
	choice := strings.ToLower(strings.TrimSpace(line))
	// EOF with an empty line (piped input that closed, Ctrl-D without input)
	// is treated as decline, same call resolveSpawnCollision makes for the
	// same shape of input.
	if choice == "n" || choice == "no" || (errors.Is(err, io.EOF) && choice == "") {
		return false, nil
	}

	if err := agentStartFn(ctx, homePath, name); err != nil {
		return false, fmt.Errorf("starting agent %q: %w", name, err)
	}
	if err := waitForAgentSession(ctx, name); err != nil {
		return false, err
	}
	return true, nil
}

// waitForAgentSession polls agentSessionReadyFn until the agent's tmux
// session exists, ctx is cancelled, or agentStartTimeout elapses. ctx
// cancellation returns ctx.Err() immediately rather than continuing to poll
// out the full timeout — an attach command whose context was cancelled
// (parent command interrupted, test deadline, etc.) should stop promptly.
func waitForAgentSession(ctx context.Context, name string) error {
	deadline := time.Now().Add(agentStartTimeout)
	for {
		if agentSessionReadyFn(name) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent %q did not become ready within %s", name, agentStartTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentStartPollInterval):
		}
	}
}
