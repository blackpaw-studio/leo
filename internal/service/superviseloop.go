package service

import (
	"context"
	"os/exec"
	"time"
)

// LoopSpec describes one tmux-hosted claude that should be kept alive.
// Both processes and persistent task sessions share this shape; the
// supervise loop is generic over the spec source.
type LoopSpec struct {
	Name         string                 // logical name; used for state/logs
	SessionName  string                 // tmux session name (e.g. "leo-foo")
	Workdir      string                 // working directory for tmux new-session
	ShellCmd     string                 // already-assembled `claude ...` command line
	OnSessionEnd func(restartCount int) // optional callback after each end
}

// loopExecCommand is the seam tests replace for tmux invocations made by
// runSuperviseLoop. This is intentionally separate from any execCommand
// var in process.go to avoid conflicting with the process supervisor's
// own testability seam.
var loopExecCommand = exec.Command

// runSuperviseLoop is the generic restart-with-backoff loop shared by
// process.go (processes) and session.go (persistent task sessions).
// Returns when ctx is cancelled.
func runSuperviseLoop(ctx context.Context, tmuxPath string, spec LoopSpec) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	restarts := 0
	for {
		if ctx.Err() != nil {
			return
		}
		// kill any stale session
		_ = loopExecCommand(tmuxPath, "-L", "leo", "kill-session", "-t", spec.SessionName).Run()

		// new-session
		cmd := loopExecCommand(tmuxPath, "-L", "leo", "new-session", "-d", "-s", spec.SessionName,
			"-c", spec.Workdir, "-x", "200", "-y", "50", spec.ShellCmd)
		if err := cmd.Run(); err != nil {
			// backoff and retry
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		// wait for session to end
		for ctx.Err() == nil {
			check := loopExecCommand(tmuxPath, "-L", "leo", "has-session", "-t", spec.SessionName)
			if err := check.Run(); err != nil {
				break // session ended
			}
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		restarts++
		if spec.OnSessionEnd != nil {
			spec.OnSessionEnd(restarts)
		}
		// exponential backoff before next restart
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}
