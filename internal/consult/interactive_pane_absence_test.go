package consult

import (
	"errors"
	"testing"
	"time"
)

// TestSweepInteractivePaneAbsenceLiveness pins the behavior a closed-by-hand
// (or killed) interactive pane must trigger: the dispatcher's periodic Sweep
// already reuses TmuxInteractiveRuntime.PanePresence (via the optional
// panePresenceRuntime interface) to detect the run's own pane going away and
// route it through the same close path used elsewhere (beginSettlementLocked
// -> finishInteractiveLocked), closing the record and releasing any leo_wait
// waiter on it. A transient probe error must never be treated as absence, and
// a genuinely present pane must leave the run untouched.
func TestSweepInteractivePaneAbsenceLiveness(t *testing.T) {
	t.Run("absent pane closes the run and releases waiters", func(t *testing.T) {
		d, _, id := releaseState(t, StatusIdle)
		now := time.Now()
		d.now = func() time.Time { return now }
		d.SetInteractiveRuntime(presenceInteractiveRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{}, presence: PaneAbsent})

		d.Sweep(now) // first tick: detects absence, begins settlement
		d.mu.Lock()
		if got := d.runs[id].record.Status; got != StatusSettling {
			d.mu.Unlock()
			t.Fatalf("status after detection = %s, want settling", got)
		}
		done := d.runs[id].done
		d.mu.Unlock()

		select {
		case <-done:
			t.Fatal("waiter released before settlement grace elapsed")
		default:
		}

		now = now.Add(finalReportGrace + time.Second)
		d.now = func() time.Time { return now }
		d.Sweep(now) // second tick: grace elapsed, finalize

		select {
		case <-done:
		default:
			t.Fatal("waiter channel not released after settlement grace elapsed")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if got := d.runs[id].record.Status; !got.Terminal() {
			t.Fatalf("final status = %s, want terminal", got)
		}
	})

	t.Run("probe error leaves the run untouched", func(t *testing.T) {
		d, _, id := releaseState(t, StatusIdle)
		now := time.Now()
		d.now = func() time.Time { return now }
		d.SetInteractiveRuntime(presenceInteractiveRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{}, probeErr: errors.New("socket unavailable")})

		d.Sweep(now)

		d.mu.Lock()
		defer d.mu.Unlock()
		if got := d.runs[id].record.Status; got != StatusIdle {
			t.Fatalf("status = %s, want unchanged idle", got)
		}
	})

	t.Run("present pane leaves the run untouched", func(t *testing.T) {
		d, _, id := releaseState(t, StatusIdle)
		now := time.Now()
		d.now = func() time.Time { return now }
		d.SetInteractiveRuntime(presenceInteractiveRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{}, presence: PanePresentAlive})

		d.Sweep(now)

		d.mu.Lock()
		defer d.mu.Unlock()
		if got := d.runs[id].record.Status; got != StatusIdle {
			t.Fatalf("status = %s, want unchanged idle", got)
		}
	})
}
