package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
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

// RestartPendingIDs returns the ids of the records the restart reconcile has
// yet to finish: housekeeping that rewrites records leaves them alone until
// it has.
func (d *Dispatcher) RestartPendingIDs() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make(map[string]bool, len(d.restartPending))
	for id := range d.restartPending {
		ids[id] = true
	}
	return ids
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

// memoRestartLister lists and resolves each repository's worktrees at most
// once for the fast pass: nothing changes worktrees before the daemon binds,
// so one listing answers every record in the repository.
func (d *Dispatcher) memoRestartLister(ctx context.Context) func(id string) worktreeLister {
	type listing struct {
		paths map[string]bool
		err   error
	}
	lists := map[string]listing{}
	return func(id string) worktreeLister {
		return func(root string) (map[string]bool, error) {
			if l, ok := lists[root]; ok {
				return l.paths, l.err
			}
			paths, err := d.restartLister(ctx, id)(root)
			lists[root] = listing{paths, err}
			return paths, err
		}
	}
}

// restartLister lists a repository's worktrees for record id's restart
// reconciliation, with the restart git timeout.
func (d *Dispatcher) restartLister(ctx context.Context, id string) worktreeLister {
	git := d.restartGit(ctx, id)
	return func(root string) (map[string]bool, error) {
		out, err := git("-C", root, "worktree", "list", "--porcelain")
		if err != nil {
			return nil, err
		}
		return resolveListedWorktrees(out), nil
	}
}

// cleanupRestartWorktree is cleanupWorktree for the restart path: every git
// call is bounded by the restart git timeout, and a removal, once begun, is
// not cut short by ctx.
func (d *Dispatcher) cleanupRestartWorktree(ctx context.Context, id string) Record {
	return d.cleanupWorktreeWith(id, d.restartGit(ctx, id), d.restartGit(context.WithoutCancel(ctx), id))
}

// RestartReconcileSummary reports what ReconcileRestart did. Records counts
// the jobs it reached, Skipped those it left to a run that took the record
// over, and Removed, Kept and Deferred the worktree outcomes of the rest. Git
// calls and timeouts count the pass's own restart-path git calls.
type RestartReconcileSummary struct {
	Records, Skipped        int
	Removed, Kept, Deferred int
	GitCalls, GitTimeouts   int64
	Sweep                   SweepSummary
	Took                    time.Duration
	Canceled                bool
}

// ReconcileRestart runs the reconciliation MarkInterrupted queued, once the
// daemon is serving: first the sweep of run files no live run owns, then,
// over workers workers, notifying records first, the records that were
// terminal at shutdown (viewer, pane and worktree reconciled, notifications
// requeued) and the worktree assessments of the runs MarkInterrupted
// settled. It returns once the queue is drained or ctx is done: it then
// stops taking jobs, and the records it did not reach stay exactly as
// loaded.
func (d *Dispatcher) ReconcileRestart(ctx context.Context, workers int) RestartReconcileSummary {
	began := time.Now()
	gitCalls, gitTimeouts := d.restartGitCalls.Load(), d.restartGitTimeouts.Load()
	summary := RestartReconcileSummary{Sweep: d.SweepRunFiles()}
	recorder, _ := d.recorder.(*FileRecorder)
	jobs := d.takeRestartJobs()
	var mu sync.Mutex
	next := 0
	take := func() (restartJob, bool) {
		mu.Lock()
		defer mu.Unlock()
		if next == len(jobs) || ctx.Err() != nil {
			return restartJob{}, false
		}
		next++
		return jobs[next-1], true
	}
	tally := func(rec Record, ran bool) {
		mu.Lock()
		defer mu.Unlock()
		summary.tally(rec, ran)
	}
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job, ok := take(); ok; job, ok = take() {
				rec, ran := d.runRestartJob(ctx, recorder, job)
				tally(rec, ran)
			}
		}()
	}
	wg.Wait()
	// A cancel that cut a job's git short counts too, though no job was
	// left untaken.
	summary.Canceled = next < len(jobs) || ctx.Err() != nil
	summary.GitCalls = d.restartGitCalls.Load() - gitCalls
	summary.GitTimeouts = d.restartGitTimeouts.Load() - gitTimeouts
	summary.Took = time.Since(began)
	return summary
}

// LogLine renders the summary for the startup log. tmuxCalls is the tmux
// call count the caller measured over the pass, which includes whatever
// else ran meanwhile, so it is approximate.
func (s RestartReconcileSummary) LogLine(tmuxCalls int64) string {
	line := fmt.Sprintf("restart reconcile: %d records (%d skipped), worktrees removed %d / kept %d / deferred %d, %d git calls (%d timed out), ~%d tmux calls, run files kept/removed %d/%d, took %s",
		s.Records, s.Skipped, s.Removed, s.Kept, s.Deferred, s.GitCalls, s.GitTimeouts, tmuxCalls, s.Sweep.Kept, s.Sweep.Removed, s.Took.Round(time.Millisecond))
	if s.Canceled {
		line += ", canceled"
	}
	return line
}

// tally counts one job's outcome: rec is the record it left, when it ran.
func (s *RestartReconcileSummary) tally(rec Record, ran bool) {
	s.Records++
	if !ran {
		s.Skipped++
		return
	}
	if rec.Isolation != "worktree" {
		return
	}
	switch rec.WorktreeState {
	case WorktreeRemoved:
		s.Removed++
	case WorktreeKept:
		s.Kept++
	case WorktreeDeferred:
		s.Deferred++
	}
}

// runRestartJob runs job under its record's serial lock, unless another run
// has taken the record over since it was queued, and returns the record it
// left and whether it ran.
func (d *Dispatcher) runRestartJob(ctx context.Context, recorder *FileRecorder, job restartJob) (Record, bool) {
	defer d.finishRestartJob(job.id)
	unlock := d.serialLocks([]string{job.id})
	defer unlock()
	d.mu.Lock()
	owned := d.runs[job.id] == job.owner
	d.mu.Unlock()
	if !owned || recorder == nil {
		return Record{}, false
	}
	rec, state, err := d.lookup(job.id)
	if err != nil {
		return Record{}, false
	}
	tm := d.newRecordTimings()
	defer d.warnSlowRecord("restart reconcile", job.id, tm)
	if job.kind == restartTerminal {
		return d.reconcileTerminalRecord(ctx, recorder, rec, tm), true
	}
	if rec.WorktreeState == WorktreeRemoved {
		return Record{}, false
	}
	stop := tm.start("worktree cleanup")
	defer stop()
	return d.applyWorktreeAssessment(d.restartGit(ctx, job.id), d.restartGit(context.WithoutCancel(ctx), job.id), rec, state), true
}

// reconcileTerminalRecord reconciles a record that was already terminal when
// the previous daemon stopped: its split viewer looked up, a pane still
// standing closed, its worktree cleaned up or deferred, and the
// notifications it still owes requeued. It returns the record it left.
func (d *Dispatcher) reconcileTerminalRecord(ctx context.Context, recorder *FileRecorder, rec Record, tm *recordTimings) Record {
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
	return rec
}
