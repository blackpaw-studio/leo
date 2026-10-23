package startup

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync/atomic"
)

// ExecCounts is a point-in-time tally of the external commands the daemon
// started through wrapped seams.
type ExecCounts struct {
	Tmux int64
	Git  int64
}

// Sub returns the calls made since an earlier snapshot.
func (c ExecCounts) Sub(earlier ExecCounts) ExecCounts {
	return ExecCounts{Tmux: c.Tmux - earlier.Tmux, Git: c.Git - earlier.Git}
}

// ExecCounter counts tmux and git command constructions, classified by the
// base name of the executable. It is safe for concurrent use.
type ExecCounter struct {
	tmux atomic.Int64
	git  atomic.Int64
}

func (c *ExecCounter) record(name string) {
	switch filepath.Base(name) {
	case "tmux":
		c.tmux.Add(1)
	case "git":
		c.git.Add(1)
	}
}

// Snapshot returns the counts so far.
func (c *ExecCounter) Snapshot() ExecCounts {
	return ExecCounts{Tmux: c.tmux.Load(), Git: c.git.Load()}
}

// WrapContext returns fn with every call counted.
func (c *ExecCounter) WrapContext(fn func(context.Context, string, ...string) *exec.Cmd) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		c.record(name)
		return fn(ctx, name, args...)
	}
}

// Wrap returns fn with every call counted.
func (c *ExecCounter) Wrap(fn func(string, ...string) *exec.Cmd) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		c.record(name)
		return fn(name, args...)
	}
}
