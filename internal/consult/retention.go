package consult

import (
	"fmt"
	"os"
	"time"
)

// retentionSweepEvery bounds how often SweepRetention reads the records.
const retentionSweepEvery = 10 * time.Minute

// SweepRetention releases the parked runs nobody followed up within
// resumableRetention of their pane closing, as leo_release would (reason
// retention): a clean worktree is removed and one holding work is kept. It
// reads the records at most once per retentionSweepEvery, so the daemon's
// housekeeping loop may call it on every tick; the first call always runs.
// Records the restart reconcile has yet to reach are left to it. Like Sweep,
// it reads the time from the dispatcher clock; the argument is kept for the
// housekeeping API.
func (d *Dispatcher) SweepRetention(_ time.Time) {
	now := d.now()
	d.mu.Lock()
	if !d.retentionSweptAt.IsZero() && now.Sub(d.retentionSweptAt) < retentionSweepEvery {
		d.mu.Unlock()
		return
	}
	d.retentionSweptAt = now
	d.mu.Unlock()
	pending := d.RestartPendingIDs()
	for _, rec := range d.Records() {
		if pending[rec.ID] || !retentionExpired(rec, now) {
			continue
		}
		if err := d.releaseExpired(rec.ID, now); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: retention release: %v\n", rec.ID, err)
		}
	}
}

// retentionExpired reports a parked run whose retention ran out at now.
func retentionExpired(rec Record, now time.Time) bool {
	return parked(rec) && !rec.EndedAt.IsZero() && !now.Before(rec.EndedAt.Add(resumableRetention))
}

// releaseExpired releases run id for retention once, under its serial lock,
// the run is still parked and expired: a follow-up that reopened it since, or
// a release under way, leaves it alone.
func (d *Dispatcher) releaseExpired(id string, now time.Time) error {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	rec, state, err := d.lookup(id)
	if err != nil || !retentionExpired(rec, now) {
		return nil
	}
	if state != nil {
		d.mu.Lock()
		busy := state.releasing || state.awaitingSlot
		d.mu.Unlock()
		if busy {
			return nil
		}
	}
	_, err = d.releaseLocked(id, nil, ReleaseReasonRetention)
	return err
}
