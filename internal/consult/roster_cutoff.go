package consult

import (
	"maps"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// sessionEndClear is the session.end reason a /clear reports.
const sessionEndClear = "clear"

// RosterCutoffs remembers, per caller bridge key, when its claude last ran
// /clear. A cleared session starts over, so the dispatches that had already
// finished by then leave its roster at once instead of lingering through
// their grace period. Dispatches still running at the clear are never
// hidden; they finish after the cutoff and age out as normal.
//
// Cutoffs live in memory only: they matter for as long as a finished
// dispatch would otherwise be shown (bridgeRecentAfterEnd and
// viewerGraceAfterEnd), so one lost to a daemon restart has already expired
// on its own. A nil *RosterCutoffs hides nothing.
type RosterCutoffs struct {
	now func() time.Time

	mu        sync.Mutex
	clearedAt map[string]time.Time
}

// NewRosterCutoffs returns cutoffs stamped by now.
func NewRosterCutoffs(now func() time.Time) *RosterCutoffs {
	return &RosterCutoffs{now: now}
}

// OnBridgeEvent records a /clear (a session.end with reason "clear") as the
// cutoff for the event's bridge key.
func (c *RosterCutoffs) OnBridgeEvent(ev bridge.Event) {
	if c == nil || ev.Name != bridge.EventSessionEnd || ev.Reason != sessionEndClear || ev.Agent == "" {
		return
	}
	at := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	next := maps.Clone(c.clearedAt)
	if next == nil {
		next = map[string]time.Time{}
	}
	next[ev.Agent] = at
	c.clearedAt = next
}

// Visible returns records without the finished dispatches their caller's
// /clear hid, in order. records is not modified.
func (c *RosterCutoffs) Visible(records []Record) []Record {
	if c == nil {
		return records
	}
	c.mu.Lock()
	cleared := c.clearedAt // replaced, never mutated
	c.mu.Unlock()
	if len(cleared) == 0 {
		return records
	}
	visible := make([]Record, 0, len(records))
	for _, rec := range records {
		if at, ok := cleared[rec.CallerBridgeKey]; ok && rec.Status.Terminal() && !rec.EndedAt.IsZero() && !rec.EndedAt.After(at) {
			continue
		}
		visible = append(visible, rec)
	}
	return visible
}
