// Package procown decides whether a process is the one a supervised agent's
// harness runs as, from a process-table snapshot. A hook the daemon relays
// carries the agent's launch token, but child processes inherit that token
// and a harness a tool starts fires the same hooks, so the token alone does
// not say whose session a hook speaks for; the process that sent it does.
package procown

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// maxDepth bounds the walk up from a reporting process: a hook is run by leo,
// at most through a shell, so a longer chain is not a hook of the harness.
const maxDepth = 8

// Proc is one row of a process table.
type Proc struct {
	PID  int
	PPID int
	Comm string
}

// ParsePS reads `ps -A -o pid= -o ppid= -o comm=` output. The command is the
// rest of the line (it may contain spaces); any row that is not that shape is
// an error, since a half-read table could vouch for the wrong process.
func ParsePS(out []byte) (map[int]Proc, error) {
	procs := map[int]Proc{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidText, rest, _ := strings.Cut(line, " ")
		ppidText, comm, _ := strings.Cut(strings.TrimSpace(rest), " ")
		comm = strings.TrimSpace(comm)
		pid, pidErr := strconv.Atoi(pidText)
		ppid, ppidErr := strconv.Atoi(ppidText)
		if pidErr != nil || ppidErr != nil || comm == "" {
			return nil, fmt.Errorf("procown: malformed ps row %q", line)
		}
		procs[pid] = Proc{PID: pid, PPID: ppid, Comm: comm}
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("procown: empty process table")
	}
	return procs, nil
}

// OwnsHarness reports whether pid, a process that reported a hook, belongs to
// the harness running as root, the agent's tmux pane process. A hook command
// runs as leo, often through a shell, so those are looked through; the
// process found is the harness that fired the hook, and it is the agent's own
// only when it is root itself, or a direct child of root when root is a
// wrapper shell. A child harness a tool started is some other process even
// when its parent is the harness (a shell that exec'd it), so only root
// itself counts when the pane runs the harness directly.
func OwnsHarness(procs map[int]Proc, root, pid int) bool {
	if root <= 0 || pid <= 0 {
		return false
	}
	cur := pid
	for depth := 0; depth <= maxDepth; depth++ {
		p, ok := procs[cur]
		if !ok {
			return false
		}
		if !transparent(p.Comm) {
			return cur == root || (p.PPID == root && isShell(procs[root].Comm))
		}
		cur = p.PPID
	}
	return false
}

// transparent is a process a hook command runs through: a shell, or leo.
func transparent(comm string) bool {
	return isShell(comm) || baseName(comm) == "leo"
}

func isShell(comm string) bool {
	switch baseName(comm) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		return true
	}
	return false
}

// baseName is the executable's name: ps shows a full path on macOS, a short
// name on Linux, and a login shell with a leading dash.
func baseName(comm string) string {
	return strings.TrimPrefix(filepath.Base(strings.TrimSpace(comm)), "-")
}
