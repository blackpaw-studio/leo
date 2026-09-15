package consult

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

const headlessProcessGrace = 10 * time.Second

// signalProcessGroup is a test seam around the sole process-group signal
// syscall. Its pid argument is always the negative process-group ID.
var signalProcessGroup = syscall.Kill

var unsafeProcessGroupSignalLog sync.Once

var errUnsafeProcessGroup = errors.New("unsafe process group")

// signalGroup refuses targets that could signal this process's own group (or
// the special process-group targets). Callers must treat its error as
// uncertainty: no signal was sent and the group's state is unknown.
func signalGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		err := fmt.Errorf("%w %d", errUnsafeProcessGroup, pgid)
		unsafeProcessGroupSignalLog.Do(func() {
			fmt.Fprintf(os.Stderr, "consult: refusing to signal unsafe process group %d\n", pgid)
		})
		return err
	}
	return signalProcessGroup(-pgid, signal)
}

// processGroupGone treats every error other than ESRCH as uncertainty. A
// cleanup that cannot prove the group is gone must keep its worktree. This
// only contains descendants that remain in the harness process group: a
// deliberate setsid-style daemon escape is not portably discoverable here.
func processGroupGone(pgid int) bool {
	return signalGroup(pgid, 0) == syscall.ESRCH
}

func waitProcessGroupGone(pgid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !processGroupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return processGroupGone(pgid)
}

// terminateHeadlessProcessGroup is used by explicit cancellation after the
// harness leader has already exited. It gives surviving descendants the same
// bounded grace used by exec.Cmd before escalating to SIGKILL.
func (d *Dispatcher) terminateHeadlessProcessGroup(state *runState) {
	d.mu.Lock()
	pgid, gone := state.pgid, state.pgidGone
	d.mu.Unlock()
	if gone {
		return
	}
	gone, _ = terminateProcessGroup(pgid, d.ProcessGroupGrace)
	d.mu.Lock()
	state.pgidGone = gone
	d.mu.Unlock()
}

func terminateProcessGroup(pgid int, grace time.Duration) (bool, error) {
	err := signalGroup(pgid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return true, nil
	}
	if errors.Is(err, errUnsafeProcessGroup) {
		return false, err
	}
	deadline := time.Now().Add(grace)
	for !processGroupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processGroupGone(pgid) {
		return true, err
	}
	killErr := signalGroup(pgid, syscall.SIGKILL)
	return waitProcessGroupGone(pgid), killErr
}
