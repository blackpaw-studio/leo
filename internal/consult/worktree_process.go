package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// processGroupPresent takes a complete process-table snapshot. A failed or
// malformed snapshot is uncertainty, never evidence that a group is empty.
func (d *Dispatcher) processGroupPresent(pgid int) (bool, bool) {
	out, err := d.ProcessCommand(context.Background(), "ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid=").Output()
	if err != nil {
		return false, false
	}
	present := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return false, false
		}
		_, err1 := strconv.Atoi(fields[0])
		group, err2 := strconv.Atoi(fields[1])
		_, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			return false, false
		}
		if group == pgid {
			present = true
		}
	}
	return present, true
}

func (d *Dispatcher) processGroupGone(pgid int) bool {
	present, known := d.processGroupPresent(pgid)
	if !known {
		return false
	}
	return !present
}

func (d *Dispatcher) waitProcessGroupGone(pgid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !d.processGroupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return d.processGroupGone(pgid)
}

func (d *Dispatcher) terminateProcessGroup(pgid int, grace time.Duration) (bool, error) {
	present, known := d.processGroupPresent(pgid)
	if !known {
		return false, errUnsafeProcessGroup
	}
	if !present {
		return true, nil
	}
	err := signalGroup(pgid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		present, known = d.processGroupPresent(pgid)
		if !known {
			return false, errUnsafeProcessGroup
		}
		if !present {
			return true, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	present, known = d.processGroupPresent(pgid)
	if !known {
		return false, errUnsafeProcessGroup
	}
	if !present {
		return true, nil
	}
	if err := signalGroup(pgid, syscall.SIGKILL); err != nil {
		return false, err
	}
	return d.waitProcessGroupGone(pgid), nil
}

// terminateRunProcessGroup serializes the temporal authority check and any
// signal with terminal publication. Once a record is terminal, no probe or
// signal may begin for its cached numeric group.
func (d *Dispatcher) terminateRunProcessGroup(state *runState) error {
	state.terminalMu.Lock()
	defer state.terminalMu.Unlock()
	d.mu.Lock()
	pgid, terminal := state.pgid, state.record.Status.Terminal()
	d.mu.Unlock()
	if terminal || pgid == 0 {
		return nil
	}
	gone, err := d.terminateProcessGroup(pgid, d.ProcessGroupGrace)
	d.mu.Lock()
	state.pgidGone = gone
	d.mu.Unlock()
	return err
}

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
