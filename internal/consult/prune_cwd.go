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
	"sync"
	"time"
)

// lsofTimeout bounds the darwin cwd scan; lsof walks every process.
var lsofTimeout = 30 * time.Second

// deferredQuietAfter is how long a deferred dispatch must have been over
// before prune will take "no process works in its worktree" as proof its
// writers are gone. A variable so tests can shorten it.
var deferredQuietAfter = 24 * time.Hour

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
// processes are unreadable, so exit 1 with at least one entry is success. A
// timeout, any other exit status or no entries at all is a failed scan: a
// listing cut short would leave unseen processes looking absent. -b keeps
// lsof from blocking in stat calls on hung remote mounts.
func runLsofCwds(ctx context.Context, command func(context.Context, string, ...string) *exec.Cmd) ([]procCwd, error) {
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := command(ctx, "lsof", "-b", "-nP", "-a", "-d", "cwd", "-Fpcn")
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("lsof: %w", ctxErr)
	}
	var exitErr *exec.ExitError
	partialOK := errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1
	if procs := parseLsofCwds(out); len(procs) > 0 && (runErr == nil || partialOK) {
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

// cwdScan runs the process working-directory scan at most once, on first
// use, and shares the result across prune's concurrent assessments.
type cwdScan struct {
	scan  func(context.Context) ([]procCwd, error)
	once  sync.Once
	procs []procCwd
	err   error
}

func newCwdScan(scan func(context.Context) ([]procCwd, error)) *cwdScan {
	if scan == nil {
		scan = osProcessCwds
	}
	return &cwdScan{scan: scan}
}

func (s *cwdScan) load() ([]procCwd, error) {
	s.once.Do(func() {
		procs, err := s.scan(context.Background())
		if err == nil && len(procs) == 0 {
			// This very process is in the table; an empty one means the scan
			// saw nothing, not that nothing is running.
			err = errors.New("no processes listed")
		}
		for i := range procs {
			procs[i].Cwd = resolvedPath(procs[i].Cwd)
		}
		s.procs, s.err = procs, err
	})
	return s.procs, s.err
}

// holder is a process whose cwd is dir or lies under it.
func (s *cwdScan) holder(dir string) (procCwd, bool, error) {
	procs, err := s.load()
	if err != nil {
		return procCwd{}, false, err
	}
	dir = resolvedPath(dir)
	for _, p := range procs {
		if pathWithin(dir, p.Cwd) {
			return p, true, nil
		}
	}
	return procCwd{}, false, nil
}

// resolvedPath is path with symlinks resolved, or cleaned when it cannot be
// (a deleted directory): lsof and /proc report physical paths, records may not.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// pathWithin reports whether path is dir or below it, by whole path
// components, so /wt/d-1 does not contain /wt/d-10.
func pathWithin(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// writerProbe is what prune uses to decide whether a deferred record's
// writers are gone: the process table, the clock and the cwd scan.
type writerProbe struct {
	ps   psFunc
	now  func() time.Time
	cwds *cwdScan
}

// quietReason is why a terminal record's worktree cannot be called quiet, or
// "" when it ended long enough ago and no live process works inside it.
func (p writerProbe) quietReason(rec Record) string {
	switch {
	case rec.EndedAt.IsZero():
		return "no end time recorded"
	case p.now().Sub(rec.EndedAt) < deferredQuietAfter:
		return fmt.Sprintf("ended < %dh ago", int(deferredQuietAfter.Hours()))
	}
	holder, found, err := p.cwds.holder(rec.Worktree)
	switch {
	case err != nil:
		return fmt.Sprintf("cwd scan unavailable: %v", err)
	case found:
		return fmt.Sprintf("process %d (%s) working in worktree", holder.PID, holder.Comm)
	}
	return ""
}
