package consult

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// startedProcessGroup is the process group of cmd, which Start has started,
// or 0 when it cannot be told. A child started with Setpgid and no Pgid
// leads a group of its own, so its pid is the group: no getpgid, which on
// macOS fails once the child has exited, unreaped (a fast run under load
// got there first and kept its clean worktree for want of a group).
func startedProcessGroup(cmd *exec.Cmd) int {
	if attr := cmd.SysProcAttr; attr != nil && attr.Setpgid && attr.Pgid == 0 {
		return cmd.Process.Pid
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0
	}
	return pgid
}

// processGroupPresent takes one complete process-table snapshot. Failure or
// malformed output is uncertainty, never evidence that a group is empty.
// This only detects descendants that remain in the harness process group: a
// deliberate setsid-style daemon escape is not portably discoverable here.
func (d *Dispatcher) processGroupPresent(pgid int) (bool, bool) {
	out, err := d.ProcessCommand(context.Background(), "ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid=").Output()
	if err != nil {
		return false, false
	}
	present := false
	rows := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return false, false
		}
		_, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		_, parentErr := strconv.Atoi(fields[2])
		if pidErr != nil || groupErr != nil || parentErr != nil {
			return false, false
		}
		rows++
		present = present || group == pgid
	}
	if rows == 0 {
		return false, false
	}
	return present, true
}
