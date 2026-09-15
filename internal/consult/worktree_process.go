package consult

import (
	"syscall"
	"time"
)

const headlessProcessGrace = 10 * time.Second

// terminateHeadlessProcessGroup is used by explicit cancellation after the
// harness leader has already exited. It gives surviving descendants the same
// bounded grace used by exec.Cmd before escalating to SIGKILL.
func (d *Dispatcher) terminateHeadlessProcessGroup(state *runState) {
	d.mu.Lock()
	pgid, gone := state.pgid, state.pgidGone
	d.mu.Unlock()
	if gone || pgid <= 0 {
		return
	}
	gone, _ = terminateProcessGroup(pgid, d.ProcessGroupGrace)
	d.mu.Lock()
	state.pgidGone = gone
	d.mu.Unlock()
}

func terminateProcessGroup(pgid int, grace time.Duration) (bool, error) {
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return true, nil
	}
	deadline := time.Now().Add(grace)
	for !processGroupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processGroupGone(pgid) {
		return true, err
	}
	killErr := syscall.Kill(-pgid, syscall.SIGKILL)
	return waitProcessGroupGone(pgid), killErr
}
