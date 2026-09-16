package consult

import (
	"errors"
	"fmt"
	"sort"
)

func (d *Dispatcher) CloseFinished(callerSessionID string) ([]Record, error) {
	records := d.Records()
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		if rec.Kind == "dispatch" && rec.CallerSessionID == callerSessionID {
			ids = append(ids, rec.ID)
		}
	}
	sort.Strings(ids)
	unlock := d.serialLocks(ids)
	defer unlock()
	affected := map[string]bool{}
	deferLayout := func(window string) error {
		if window != "" {
			affected[window] = true
		}
		return nil
	}
	closed := make([]Record, 0)
	var errs []error
	for _, id := range ids {
		rec, _, err := d.lookup(id)
		if err != nil || rec.CallerSessionID != callerSessionID || rec.Kind != "dispatch" {
			continue
		}
		if rec.Mode == ModeInteractive {
			if rec.Status != StatusIdle && !rec.Status.Terminal() {
				continue
			}
			if rec.Status == StatusReleased {
				continue
			}
			rec, err = d.releaseLocked(id, deferLayout)
		} else {
			if !rec.Status.Terminal() {
				continue
			}
			d.mu.Lock()
			closeViewer := d.closeFinishedViewer
			d.mu.Unlock()
			if closeViewer != nil {
				rec, err = closeViewer(rec, deferLayout)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("close finished dispatch %s: %w", id, err))
			continue
		}
		closed = append(closed, rec)
	}
	d.mu.Lock()
	rt := d.interactiveRuntime
	d.mu.Unlock()
	if rt != nil {
		layout := runtimeLayout(rt)
		windows := make([]string, 0, len(affected))
		for window := range affected {
			windows = append(windows, window)
		}
		sort.Strings(windows)
		for _, window := range windows {
			if err := layout(window); err != nil {
				errs = append(errs, fmt.Errorf("reapply layout %s: %w", window, err))
			}
		}
	}
	return closed, errors.Join(errs...)
}
