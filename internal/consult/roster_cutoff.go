package consult

import (
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// sessionEndClear is the session.end reason a /clear reports.
const sessionEndClear = "clear"

// cutoffRetention is how long a cutoff can still matter: past the longest
// window a finished dispatch is shown, every record ended before the cutoff
// has aged out on its own.
const cutoffRetention = max(bridgeRecentAfterEnd, viewerGraceAfterEnd)

// rosterCutoff is one caller's last recorded /clear and the hub event that
// reported it.
type rosterCutoff struct {
	at      time.Time
	eventID string
}

// RosterCutoffs remembers, per caller bridge key, when its claude last ran
// /clear. A cleared session starts over, so the dispatches that had already
// finished by then leave its roster at once instead of lingering through
// their grace period. Dispatches still running at the clear are never
// hidden; they finish after the cutoff and age out as normal.
//
// Cutoffs live in memory only: they matter for as long as a finished
// dispatch would otherwise be shown (cutoffRetention), so one lost to a
// daemon restart has already expired on its own, and one older than that is
// pruned the next time any clear is recorded. A nil *RosterCutoffs hides
// nothing.
type RosterCutoffs struct {
	now func() time.Time

	mu        sync.Mutex
	clearedAt map[string]rosterCutoff
}

// NewRosterCutoffs returns cutoffs stamped by now.
func NewRosterCutoffs(now func() time.Time) *RosterCutoffs {
	return &RosterCutoffs{now: now}
}

// OnBridgeEvent records a /clear (a session.end with reason "clear") as the
// cutoff for the event's bridge key. The hub may redeliver an event, so a
// repeat of the key's last applied EventID is ignored rather than moving the
// cutoff past dispatches that finished since the real clear.
func (c *RosterCutoffs) OnBridgeEvent(ev bridge.Event) {
	if c == nil || ev.Name != bridge.EventSessionEnd || ev.Reason != sessionEndClear || ev.Agent == "" {
		return
	}
	at := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.clearedAt[ev.Agent]; ok && ev.EventID != "" && last.eventID == ev.EventID {
		return
	}
	next := make(map[string]rosterCutoff, len(c.clearedAt)+1)
	for key, cutoff := range c.clearedAt {
		if key != ev.Agent && at.Sub(cutoff.at) < cutoffRetention {
			next[key] = cutoff
		}
	}
	next[ev.Agent] = rosterCutoff{at: at, eventID: ev.EventID}
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
		if cutoff, ok := cleared[rec.CallerBridgeKey]; ok && rec.Status.Terminal() && !rec.EndedAt.IsZero() && !rec.EndedAt.After(cutoff.at) {
			continue
		}
		visible = append(visible, rec)
	}
	return visible
}
