package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// restartGitTimeout bounds each git call a restarted daemon makes while it
// reconciles the previous daemon's records. A call that runs past it leaves
// that record's worktree in place: a timeout never deletes anything.
const restartGitTimeout = 30 * time.Second

// restartGit runs git for record id's restart reconciliation through the
// GitCommand seam, each call bounded by the restart git timeout and counted.
// A timed-out call is logged and fails, which the safety rules read as
// unverifiable, so the worktree is kept.
func (d *Dispatcher) restartGit(ctx context.Context, id string) gitFunc {
	timeout := d.restartGitTimeout
	if timeout <= 0 {
		timeout = restartGitTimeout
	}
	return func(args ...string) ([]byte, error) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		d.restartGitCalls.Add(1)
		out, err := execGitContext(callCtx, d.GitCommand, args...)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			d.restartGitTimeouts.Add(1)
			d.restartLogf("restart reconcile: git timed out for %s (%s): worktree left in place", id, strings.Join(args, " "))
		}
		return out, err
	}
}

// restartLogf writes a restart reconciliation diagnostic to the startup log,
// or to stderr when none is set.
func (d *Dispatcher) restartLogf(format string, args ...any) {
	if d.startupLog != nil {
		d.startupLog(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
