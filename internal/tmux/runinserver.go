// Running a one-shot command *inside* leo's tmux server.
//
// Some diagnostics are only meaningful when observed from within the tmux
// process tree rather than from a leo CLI process. macOS Local Network access
// is the motivating case: TCC attributes a connection to the *responsible*
// process, so leo's own binary always passes its own probe while third-party
// binaries under agent sessions can be silently denied. A probe that
// discriminates has to run where the agents run — as a child of the tmux
// server — which is what RunInServer provides.
package tmux

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// probeSessionPrefix names throwaway sessions created by RunInServer.
// Deliberately NOT the "leo-<name>" agent session shape (see agent.SessionName)
// so nothing enumerating sessions mistakes a probe for an agent.
const probeSessionPrefix = "leo-doctor-probe-"

// probePoll is how often RunInServer checks whether the probe session has
// exited. A var so tests can shrink it.
var probePoll = 50 * time.Millisecond

// RunInServer runs shellCmd to completion in a throwaway session on leo's
// socket and returns its combined output.
//
// The command is spawned by the tmux SERVER, so it inherits exactly the
// process-creation context agent panes get — which is the whole point. Output
// comes back through a temp file rather than capture-pane: capture-pane
// reflows and truncates to the pane geometry, which mangles anything longer
// than a short line.
//
// The session is always killed and the capture file always removed, including
// on timeout. Returns an error if no server is listening, if tmux refuses the
// new-session, or if shellCmd hasn't exited within timeout.
func RunInServer(tmuxPath, shellCmd string, timeout time.Duration) (string, error) {
	if !ServerRunning(tmuxPath) {
		return "", fmt.Errorf("no tmux server running on leo's socket")
	}

	session := fmt.Sprintf("%s%d", probeSessionPrefix, os.Getpid())

	// A fresh O_EXCL temp file rather than a path derived from the session
	// name: the pane command is a shell redirect, and a predictable name would
	// rely on every previous run's deferred cleanup having actually happened.
	capture, err := os.CreateTemp("", probeSessionPrefix)
	if err != nil {
		return "", fmt.Errorf("creating probe capture file: %w", err)
	}
	capturePath := capture.Name()
	_ = capture.Close() // only the name is needed; the pane's shell writes it
	defer func() { _ = os.Remove(capturePath) }()
	defer func() {
		_ = serverExecCommand(tmuxPath, Args("kill-session", "-t", Target(session))...).Run()
	}()

	// Wrapped in sh -c so the redirection applies to shellCmd as a whole, and
	// single-token so tmux hands the entire expression to one shell.
	wrapped := fmt.Sprintf("%s > %s 2>&1", shellCmd, capturePath)
	out, err := serverExecCommand(tmuxPath,
		Args("new-session", "-d", "-s", session, "sh", "-c", wrapped)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("starting probe session: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if !waitForSessionGone(tmuxPath, session, timeout) {
		return "", fmt.Errorf("probe command timed out after %s", timeout)
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		// waitForSessionGone can't tell "the probe session exited" from "the
		// server died under us" — both make has-session fail. Distinguish here
		// so the operator isn't told the probe produced no output when the
		// truth is the tmux server vanished mid-probe.
		if !ServerRunning(tmuxPath) {
			return "", fmt.Errorf("tmux server exited during the probe")
		}
		return "", fmt.Errorf("reading probe output: %w", err)
	}
	return string(captured), nil
}

// waitForSessionGone polls has-session until the probe session disappears
// (its command exited) or the budget runs out.
func waitForSessionGone(tmuxPath, session string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if serverExecCommand(tmuxPath, Args("has-session", "-t", Target(session))...).Run() != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(probePoll)
	}
}
