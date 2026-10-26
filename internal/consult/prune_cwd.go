package consult

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// lsofTimeout bounds the darwin cwd scan; lsof walks every process.
const lsofTimeout = 30 * time.Second

// procCwd is one process and the directory it is working in.
type procCwd struct {
	PID  int
	Comm string
	Cwd  string
}

// parseLsofCwds reads `lsof -Fpcn` output: a "p<pid>" line opens a process,
// "c<comm>" names it and "n<path>" is its cwd. A process without a cwd line
// (unreadable) or with a malformed pid yields nothing. A path containing a
// newline is not representable in this format and is not matched.
func parseLsofCwds(out []byte) []procCwd {
	var procs []procCwd
	var cur procCwd
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		field, value := line[0], line[1:]
		switch field {
		case 'p':
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				pid = 0
			}
			cur = procCwd{PID: pid}
		case 'c':
			cur.Comm = value
		case 'n':
			if cur.PID > 0 {
				cur.Cwd = value
				procs = append(procs, cur)
				cur.PID = 0
			}
		}
	}
	return procs
}

// runLsofCwds lists every process's cwd with lsof. lsof exits 1 when some
// processes are unreadable, so output with at least one entry is success and
// no entries is an error.
func runLsofCwds(ctx context.Context, command func(context.Context, string, ...string) *exec.Cmd) ([]procCwd, error) {
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := command(ctx, "lsof", "-nP", "-a", "-d", "cwd", "-Fpcn")
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	procs := parseLsofCwds(out)
	if len(procs) > 0 {
		return procs, nil
	}
	if runErr == nil {
		runErr = errors.New("no process working directories in output")
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		runErr = fmt.Errorf("%w: %s", runErr, msg)
	}
	return nil, fmt.Errorf("lsof: %w", runErr)
}

// readProcCwds reads every process's cwd from a procfs mount. A process whose
// cwd cannot be read (another user's, or already gone) is skipped; only an
// unreadable procDir is an error.
func readProcCwds(procDir string) ([]procCwd, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", procDir, err)
	}
	var procs []procCwd
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(procDir, e.Name(), "cwd"))
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(procDir, e.Name(), "comm"))
		procs = append(procs, procCwd{PID: pid, Comm: strings.TrimSpace(string(comm)), Cwd: cwd})
	}
	return procs, nil
}
