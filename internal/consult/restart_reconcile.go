package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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

// restartJobKind is what ReconcileRestart does for one record.
type restartJobKind int

const (
	// restartTerminal runs the whole reconciliation of a record that was
	// already terminal when the previous daemon stopped.
	restartTerminal restartJobKind = iota
	// restartAssess assesses, and removes when safe, the worktree of a run
	// MarkInterrupted settled.
	restartAssess
)

// restartJob is one record's share of the background restart reconcile.
// owner is the record's run in d.runs when the job was queued (nil for a
// record only on disk): a job whose record has since been taken over by
// another run is skipped, since that run's own terminal transitions clean up.
type restartJob struct {
	id     string
	kind   restartJobKind
	owner  *runState
	notify bool
}

// enqueueRestart queues kind for record id, owned by its current run.
// notify marks a record with notifications still to deliver, which goes
// first.
func (d *Dispatcher) enqueueRestart(id string, kind restartJobKind, notify bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.restartPending == nil {
		d.restartPending = make(map[string]int)
	}
	d.restartJobs = append(d.restartJobs, restartJob{id: id, kind: kind, owner: d.runs[id], notify: notify})
	d.restartPending[id]++
}

// takeRestartJobs hands over the queued jobs, notifying records first.
func (d *Dispatcher) takeRestartJobs() []restartJob {
	d.mu.Lock()
	defer d.mu.Unlock()
	jobs := d.restartJobs
	d.restartJobs = nil
	slices.SortStableFunc(jobs, func(a, b restartJob) int {
		switch {
		case a.notify == b.notify:
			return 0
		case a.notify:
			return -1
		}
		return 1
	})
	return jobs
}

// finishRestartJob drops one job of id from the pending set.
func (d *Dispatcher) finishRestartJob(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.restartPending[id] <= 1 {
		delete(d.restartPending, id)
		return
	}
	d.restartPending[id]--
}

// memoRestartLister lists each repository's worktrees at most once for the
// fast pass: nothing changes worktrees before the daemon binds, so one
// listing answers every record in the repository.
func (d *Dispatcher) memoRestartLister(ctx context.Context) func(id string) worktreeLister {
	type listing struct {
		out []byte
		err error
	}
	lists := map[string]listing{}
	return func(id string) worktreeLister {
		return func(root string) ([]byte, error) {
			if l, ok := lists[root]; ok {
				return l.out, l.err
			}
			out, err := d.restartLister(ctx, id)(root)
			lists[root] = listing{out, err}
			return out, err
		}
	}
}

// restartLister lists a repository's worktrees for record id's restart
// reconciliation, with the restart git timeout.
func (d *Dispatcher) restartLister(ctx context.Context, id string) worktreeLister {
	git := d.restartGit(ctx, id)
	return func(root string) ([]byte, error) { return git("-C", root, "worktree", "list", "--porcelain") }
}

// cleanupRestartWorktree is cleanupWorktree for the restart path: every git
// call is bounded by the restart git timeout, and a removal, once begun, is
// not cut short by ctx.
func (d *Dispatcher) cleanupRestartWorktree(ctx context.Context, id string) Record {
	return d.cleanupWorktreeWith(id, d.restartGit(ctx, id), d.restartGit(context.WithoutCancel(ctx), id))
}

// RestartReconcileSummary reports what ReconcileRestart did.
type RestartReconcileSummary struct {
	Records, Skipped int
	Took             time.Duration
	Canceled         bool
}

// ReconcileRestart runs the reconciliation MarkInterrupted queued, once the
// daemon is serving: records that were terminal at shutdown get their
// viewer, pane and worktree reconciled and their notifications requeued, and
// runs MarkInterrupted settled get their worktrees assessed. It returns once
// the queue is drained or ctx is done; records it did not reach stay
// exactly as they were.
func (d *Dispatcher) ReconcileRestart(ctx context.Context, workers int) RestartReconcileSummary {
	began := time.Now()
	var summary RestartReconcileSummary
	recorder, _ := d.recorder.(*FileRecorder)
	for _, job := range d.takeRestartJobs() {
		if ctx.Err() != nil {
			summary.Canceled = true
			break
		}
		summary.Records++
		if !d.runRestartJob(ctx, recorder, job) {
			summary.Skipped++
		}
	}
	summary.Took = time.Since(began)
	return summary
}

// runRestartJob runs job under its record's serial lock, unless another run
// has taken the record over since it was queued, and reports whether it ran.
func (d *Dispatcher) runRestartJob(ctx context.Context, recorder *FileRecorder, job restartJob) bool {
	defer d.finishRestartJob(job.id)
	unlock := d.serialLocks([]string{job.id})
	defer unlock()
	d.mu.Lock()
	owned := d.runs[job.id] == job.owner
	d.mu.Unlock()
	if !owned || recorder == nil {
		return false
	}
	rec, state, err := d.lookup(job.id)
	if err != nil {
		return false
	}
	tm := d.newRecordTimings()
	defer d.warnSlowRecord("restart reconcile", job.id, tm)
	if job.kind == restartTerminal {
		d.reconcileTerminalRecord(ctx, recorder, rec, tm)
		return true
	}
	if rec.WorktreeState == WorktreeRemoved {
		return false
	}
	stop := tm.start("worktree cleanup")
	d.applyWorktreeAssessment(d.restartGit(ctx, job.id), d.restartGit(context.WithoutCancel(ctx), job.id), rec, state)
	stop()
	return true
}

// reconcileTerminalRecord reconciles a record that was already terminal when
// the previous daemon stopped: its split viewer looked up, a pane still
// standing closed, its worktree cleaned up or deferred, and the
// notifications it still owes requeued.
func (d *Dispatcher) reconcileTerminalRecord(ctx context.Context, recorder *FileRecorder, rec Record, tm *recordTimings) {
	stop := tm.start("viewer pane lookup")
	rec = d.reconcileRestartViewer(rec)
	stop()
	if rec.WorktreeState == WorktreeCreating {
		stop = tm.start("worktree reconcile")
		rec = d.reconcileCreating(rec, d.restartLister(ctx, rec.ID))
		stop()
	}
	writerUncertain := false
	if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved && rec.Mode != ModeInteractive {
		writerUncertain = true
	}
	// A released run's pane was killed (release fails otherwise); any
	// other interactive run without a recorded pane may still have an
	// unrecorded one.
	if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved && rec.Mode == ModeInteractive && rec.PaneID == "" && rec.Status != StatusReleased {
		writerUncertain = true
	}
	if rec.Mode == ModeInteractive && rec.PaneID != "" && d.interactiveRuntime != nil {
		stop = tm.start("pane presence")
		alive, certain := panePresent(d.interactiveRuntime, rec.PaneID)
		stop()
		if !certain && rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved {
			writerUncertain = true
		} else if alive {
			stop = tm.start("pane close")
			cleaned, killErr := d.closeRestartPane(rec)
			stop()
			if killErr != nil {
				if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved {
					rec.WorktreeState = WorktreeDeferred
					writerUncertain = true
				}
				d.trackRestartKill(rec)
			} else {
				rec = cleaned
				_ = writeRecord(recorder.dir, rec)
			}
		}
	}
	stop = tm.start("worktree cleanup")
	if writerUncertain {
		rec = d.setWorktreeState(rec, nil, WorktreeDeferred)
	} else {
		rec = d.cleanupRestartWorktree(ctx, rec.ID)
	}
	stop()
	stop = tm.start("notification restore")
	d.restorePendingNotifications(rec)
	stop()
}
