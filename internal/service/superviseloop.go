package service

import (
	"context"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// LoopSpec describes one tmux-hosted claude that should be kept alive.
// Both processes and persistent task sessions share this shape; the
// supervise loop is generic over the spec source.
type LoopSpec struct {
	Name        string // logical name; used for state/logs
	SessionName string // tmux session name (e.g. "leo-foo")
	Workdir     string // working directory for tmux new-session
	// ShellCmd assembles the `claude ...` command line. resume=false omits
	// --resume so a poisoned session can recover by starting fresh after a
	// quick exit.
	ShellCmd func(resume bool) string
	// OnQuickExit fires once when claude exits faster than quickExitThreshold
	// (poisoned-session recovery): the caller should clear any stored resume
	// state. Optional.
	OnQuickExit  func()
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
	// quickExitThreshold: a claude that dies faster than this is treated as a
	// poisoned session — strip --resume and clear the stored id so the next
	// spawn starts fresh, instead of crash-looping on the same stale id.
	const quickExitThreshold = 15 * time.Second
	restarts := 0
	resume := true
	for {
		if ctx.Err() != nil {
			return
		}
		// kill any stale session
		_ = loopExecCommand(tmuxPath, "-L", "leo", "kill-session", "-t", tmux.Target(spec.SessionName)).Run()

		// new-session
		start := time.Now()
		cmd := loopExecCommand(tmuxPath, "-L", "leo", "new-session", "-d", "-s", spec.SessionName,
			"-c", spec.Workdir, "-x", "200", "-y", "50", spec.ShellCmd(resume))
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
			check := loopExecCommand(tmuxPath, "-L", "leo", "has-session", "-t", tmux.Target(spec.SessionName))
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
		// Poisoned-session recovery: a very-quick exit while resuming means the
		// stored session id is likely stale. Drop resume for the next spawn and
		// let the caller clear the stored id. Only fires once (guarded by
		// `resume`) so a genuinely broken command doesn't repeatedly clear.
		if resume && time.Since(start) < quickExitThreshold {
			resume = false
			if spec.OnQuickExit != nil {
				spec.OnQuickExit()
			}
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
