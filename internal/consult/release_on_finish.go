package consult

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ResolveReleaseOnFinish is a dispatch's release_on_finish setting: the
// explicit value when given, else on for the read-only roles (which rarely
// get a follow-up) and off for every other dispatch, role-less included.
func ResolveReleaseOnFinish(role string, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return config.IsReadOnlyRole(role)
}

// releasableOnFinishLocked reports whether rec is an interactive run that
// opted into release_on_finish and is cleanly done: exactly one turn, an
// orchestrator-sent one that finished, nothing queued or pending, and the
// run idle (so not needs_input, waiting or settling). Another turn, a typed
// one included, means someone is still using the pane. The caller holds d.mu.
func (d *Dispatcher) releasableOnFinishLocked(s *runState) bool {
	rec := s.record
	if rec.Mode != ModeInteractive || !rec.ReleaseOnFinish || rec.Status != StatusIdle || s.releasing || s.queuedSend != nil || s.awaitingSlot {
		return false
	}
	if len(rec.Turns) != 1 || rec.NeedsInput != nil || len(s.permissions) > 0 || rec.PendingWork != nil {
		return false
	}
	first := rec.Turns[0]
	return first.Source == TurnSourceOrchestrator && first.Outcome == TurnFinished && !first.Queued && first.Pending == nil
}

// releaseDeliveredLocked releases run id once its result reached the caller.
// The caller holds the run's serial lock and has just delivered or returned
// the result; every other condition is judged here, under d.mu, so a state
// that changed since (a follow-up, a permission request) wins.
func (d *Dispatcher) releaseDeliveredLocked(id string) error {
	d.mu.Lock()
	s := d.runs[id]
	if s == nil || !d.releasableOnFinishLocked(s) {
		d.mu.Unlock()
		return nil
	}
	s.releasingOnFinish = true
	d.mu.Unlock()
	_, err := d.releaseLocked(id, nil)
	d.mu.Lock()
	s.releasingOnFinish = false
	d.mu.Unlock()
	if err != nil {
		return fmt.Errorf("release %s after finishing: %w", id, err)
	}
	return nil
}

// releasedSendError explains a send to a released run. Only a run that
// released itself says why; a manually released run keeps its generic error.
func releasedSendError(rec Record) error {
	if rec.ReleasedOnFinish {
		return fmt.Errorf("dispatch %s was released after finishing (release_on_finish); start a new dispatch", rec.ID)
	}
	return nil
}

// WaitAndRelease is Wait followed by deliver, the transport write of the
// entries to the caller. Only once deliver succeeded with ctx still live does
// it release the release_on_finish runs whose complete finished result those
// entries carried and collect them; a failed write or canceled request keeps
// every run. Wait itself never releases.
func (d *Dispatcher) WaitAndRelease(ctx context.Context, ids []string, timeout time.Duration, deliver func([]Entry) error) error {
	entries := d.Wait(ctx, ids, timeout)
	if err := deliver(entries); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	d.releaseReturned(ids, entries)
	return nil
}

func (d *Dispatcher) releaseReturned(ids []string, entries []Entry) {
	unlock := d.serialLocks(ids)
	defer unlock()
	for i, e := range entries {
		if e.Outcome != TurnFinished || e.Status != StatusIdle || e.truncated {
			continue
		}
		runID := strings.SplitN(ids[i], "#", 2)[0]
		if err := d.releaseDeliveredLocked(runID); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: %v\n", runID, err)
			continue
		}
		d.mu.Lock()
		state := d.runs[runID]
		released := state != nil && state.record.Status == StatusReleased
		var done chan struct{}
		if state != nil {
			done = state.done
		}
		d.mu.Unlock()
		if released {
			d.collectRun(context.Background(), runID, state, done)
		}
	}
}
