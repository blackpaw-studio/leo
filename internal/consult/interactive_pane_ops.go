package consult

import "time"

// paneOp is one pane-affecting step for an interactive run: publishing its
// launched pane, reconciling its placement (hide/show), or killing it. A run's ops execute one at
// a time, in the order they were queued, and each decides what to do from
// the run's state when it executes, never from a snapshot taken when it was
// queued. Status transitions (hooks, Send, Cancel) only change state and
// queue ops, so no transition can race a pane move or kill already under
// way: whatever it changes is seen by the next op in line.
type paneOp struct {
	kind paneOpKind
	run  func()
	done chan struct{}
}

type paneOpKind uint8

const (
	paneOpPublish paneOpKind = iota
	paneOpReconcile
	paneOpKill
)

// defaultPaneOpWait bounds how long a cancellation waits for its kill to
// run. A wedged tmux must not wedge the cancel; the kill stays queued, and
// any pane published meanwhile sees the cancellation and kills itself.
const defaultPaneOpWait = 15 * time.Second

// enqueuePaneOpLocked queues run on s's pane-op worker, starting the worker
// if it is not running, and returns a channel closed once run has returned.
func (d *Dispatcher) enqueuePaneOpLocked(s *runState, kind paneOpKind, run func()) <-chan struct{} {
	op := paneOp{kind: kind, run: run, done: make(chan struct{})}
	s.paneOps = append(s.paneOps, op)
	if !s.paneWorker {
		s.paneWorker = true
		go d.drainPaneOps(s)
	}
	return op.done
}

// drainPaneOps runs s's queued ops in order and exits once none remain; the
// next enqueue starts a new worker. At most one worker runs per run.
func (d *Dispatcher) drainPaneOps(s *runState) {
	for {
		d.mu.Lock()
		if len(s.paneOps) == 0 {
			s.paneWorker = false
			d.mu.Unlock()
			return
		}
		op := s.paneOps[0]
		s.paneOps = s.paneOps[1:]
		d.mu.Unlock()
		op.run()
		close(op.done)
	}
}

func (d *Dispatcher) paneOpQueuedLocked(s *runState, kind paneOpKind) bool {
	for _, op := range s.paneOps {
		if op.kind == kind {
			return true
		}
	}
	return false
}

// awaitPaneOp waits for done up to the dispatcher's pane-op wait.
func (d *Dispatcher) awaitPaneOp(done <-chan struct{}) {
	wait := d.paneOpWait
	if wait <= 0 {
		wait = defaultPaneOpWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// killRunPane is a cancellation's kill: whatever pane the run has when the
// op runs, if it is still alive.
func (d *Dispatcher) killRunPane(s *runState) {
	d.mu.Lock()
	rt, pane, rec := d.interactiveRuntime, s.record.PaneID, cloneRecord(s.record)
	d.mu.Unlock()
	if pane != "" && rt != nil && rt.Alive(pane) {
		_, _ = d.closeRecordedPane(rec, pane, rt.Kill, runtimeLayout(rt))
	}
}

// closeStalePane kills pane if, now, the run has settled or moved on to a
// different pane: an opening that outlived its run.
func (d *Dispatcher) closeStalePane(s *runState, rt InteractiveRuntime, pane string) {
	d.mu.Lock()
	stale := s.killRequested || s.record.Status.Terminal() || s.record.Status == StatusSettling || s.record.PaneID != pane
	rec := cloneRecord(s.record)
	d.mu.Unlock()
	if stale {
		_, _ = d.closeRecordedPane(rec, pane, rt.Kill, runtimeLayout(rt))
	}
}

// retryPaneKill retries a kill that failed earlier (killPending), unless the
// run has since been released or its pane is already gone.
func (d *Dispatcher) retryPaneKill(s *runState, rt InteractiveRuntime) {
	d.mu.Lock()
	if s.releasing || s.record.Status == StatusReleased || !s.killPending || s.record.PaneID == "" {
		d.mu.Unlock()
		return
	}
	rec, pane := cloneRecord(s.record), s.record.PaneID
	d.mu.Unlock()
	kill := rt.Kill
	if prober, ok := rt.(panePresenceRuntime); ok {
		presence, err := prober.PanePresence(pane)
		if err != nil {
			return
		}
		if presence == PaneAbsent {
			kill = func(string) error { return nil }
		}
	}
	_, _ = d.closeRecordedPane(rec, pane, kill, runtimeLayout(rt))
}
