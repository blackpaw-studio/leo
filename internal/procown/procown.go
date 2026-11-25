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

// OwnsHarness reports whether pid, the process the kernel says sent a relay,
// belongs to the harness running as root, the agent's tmux pane process. From
// pid it walks up (pid included: an opencode plugin runs inside its harness)
// to the first process that is a harness, and the relay is the agent's own
// only if that is root's harness. A harness the agent started is the first
// harness above its own relay, whatever launched it, so it is refused; so is
// any chain with no harness in it, and any process missing from the table.
//
// A harness is claude, codex, opencode, or the binary root itself runs (an
// agent whose harness is none of those is still told apart from one it starts
// of the same kind). When the pane runs a wrapper shell, root's harness is the
// recognised harness directly under it.
func OwnsHarness(procs map[int]Proc, root, pid int) bool {
	if root <= 0 || pid <= 0 {
		return false
	}
	rootHarness, ok := harnessOf(procs, root)
	if !ok {
		return false
	}
	own := baseName(procs[rootHarness].Comm)
	// Trust boundary: the agent's own process tree under its pane-root harness; a nested harness is the first harness above its own relay, so it is refused.
	cur := pid
	for depth := 0; depth <= maxDepth; depth++ {
		p, ok := procs[cur]
		if !ok {
			return false
		}
		if isHarness(p.Comm, own) {
			return cur == rootHarness
		}
		cur = p.PPID
	}
	return false
}

// harnessOf is the process that is the harness of the pane running as root:
// root itself, or, under a wrapper shell, the one recognised harness it runs.
func harnessOf(procs map[int]Proc, root int) (int, bool) {
	r, ok := procs[root]
	if !ok {
		return 0, false
	}
	if !isShell(r.Comm) {
		return root, true
	}
	found := 0
	for pid, p := range procs {
		if p.PPID == root && isHarness(p.Comm, "") {
			if found != 0 {
				return 0, false // ambiguous: fail closed
			}
			found = pid
		}
	}
	return found, found != 0
}

// isHarness is whether comm names a harness: one leo supervises, or own.
func isHarness(comm, own string) bool {
	name := baseName(comm)
	switch name {
	case "claude", "codex", "opencode":
		return true
	}
	return own != "" && name == own
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
