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

type processInfo struct{ pid, pgid, ppid int }

// processGroupOwnership takes a fresh process-table snapshot before signalling.
// A recycled group is not owned merely because its numeric ID matches a
// cached PGID; at least one member must descend from the harness or daemon.
func (d *Dispatcher) processGroupOwnership(pgid, harnessPID int) (bool, bool, bool) {
	out, err := d.ProcessCommand(context.Background(), "ps", "-o", "pid=,pgid=,ppid=").Output()
	if err != nil {
		return false, false, false
	}
	processes := make(map[int]processInfo)
	groupPresent := false
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		group, err2 := strconv.Atoi(fields[1])
		parent, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		processes[pid] = processInfo{pid, group, parent}
		groupPresent = groupPresent || group == pgid
	}
	for _, process := range processes {
		if process.pgid != pgid {
			continue
		}
		seen := map[int]bool{}
		for pid := process.pid; pid > 1 && !seen[pid]; {
			if pid == harnessPID || pid == os.Getpid() {
				return true, true, true
			}
			seen[pid] = true
			parent, ok := processes[pid]
			if !ok {
				break
			}
			pid = parent.ppid
		}
		if process.ppid == harnessPID || process.ppid == os.Getpid() {
			return true, true, true
		}
	}
	return false, groupPresent, true
}

func (d *Dispatcher) ownedSignal(pgid, harnessPID int, signal syscall.Signal) error {
	owned, _, _ := d.processGroupOwnership(pgid, harnessPID)
	if !owned {
		return errUnsafeProcessGroup
	}
	return signalGroup(pgid, signal)
}

func (d *Dispatcher) ownedProcessGroupGone(pgid, harnessPID int) bool {
	owned, present, known := d.processGroupOwnership(pgid, harnessPID)
	if !known {
		return false
	}
	if !present {
		return true
	}
	if !owned {
		return false
	}
	return signalGroup(pgid, 0) == syscall.ESRCH
}

func (d *Dispatcher) waitOwnedProcessGroupGone(pgid, harnessPID int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !d.ownedProcessGroupGone(pgid, harnessPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return d.ownedProcessGroupGone(pgid, harnessPID)
}

func (d *Dispatcher) terminateOwnedProcessGroup(pgid, harnessPID int, grace time.Duration) (bool, error) {
	err := d.ownedSignal(pgid, harnessPID, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(grace)
	for !d.ownedProcessGroupGone(pgid, harnessPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if d.ownedProcessGroupGone(pgid, harnessPID) {
		return true, nil
	}
	if err := d.ownedSignal(pgid, harnessPID, syscall.SIGKILL); err != nil {
		return false, err
	}
	return d.waitOwnedProcessGroupGone(pgid, harnessPID), nil
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
