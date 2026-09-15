package consult

import (
	"context"
	"strconv"
	"strings"
)

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
