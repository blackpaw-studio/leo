package consult

import (
	"fmt"
	"testing"
	"time"
)

// A wake (a background task's notification) that reaches leo before the
// delayed Stop of the turn it continues is that turn's, not a new one.
func TestPromptIDWakeBeforeTheWorkingTurnsPendingWorkStopAliasesIt(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idSubmit(t, "u2", "w", taskNotification))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" || rec.Steered {
		t.Fatalf("the wake opened a turn of its own: %+v", rec)
	}
	reportAll(t, d, id, idStopWaiting(t, "s1", "a", "I'll wait for the build."))
	rec = idleRecord(t, d, id)
	if rec.Status != StatusWaiting || rec.Turns[0].Outcome != "" {
		t.Fatalf("a stop with pending work, after the wake, did not keep the turn waiting: %+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s2", "w", "Build passed."))
	rec = idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "Build passed." || rec.Status != StatusIdle {
		t.Fatalf("the wake's stop did not finish the turn: %+v", rec)
	}
}

// A wake with no turn running is Claude starting a turn on its own.
func TestPromptIDWakeWithNothingWorkingOpensItsOwnTurn(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening"), idSubmit(t, "u2", "w", taskNotification))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 2 || rec.Turns[1].Source != TurnSourceUser || rec.Steered {
		t.Fatalf("record=%+v", rec)
	}
}

// A finished turn's ids stay retired for as long as the turn is in the
// run's record, however many turns came after it: a replayed submit for an
// old turn opens no new one.
func TestPromptIDClosedIDsOutliveTheDedupWindow(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = sharedClock(&now)
	s := &runState{record: Record{ID: "d-old", Harness: "claude", Mode: ModeInteractive, Status: StatusIdle}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}}
	d.mu.Lock()
	d.runs[s.record.ID] = s
	for i := 0; i <= maxInteractiveDedup; i++ {
		pid := fmt.Sprintf("h%d", i)
		turn := d.openTurnLocked(s, TurnSourceUser, "", false)
		d.bindKeyLocked(s, turn.TurnID, "p:"+pid)
		d.closeTurnLocked(s, turn.TurnID, TurnFinished, "reply")
	}
	d.mu.Unlock()
	reportAll(t, d, s.record.ID, idSubmit(t, "replay-h0", "h0", "typed"), idStop(t, "replay-s-h0", "h0", "reply"))
	rec := idleRecord(t, d, s.record.ID)
	if len(rec.Turns) != maxInteractiveDedup+1 || rec.Status != StatusIdle {
		t.Fatalf("a replay of the oldest turn's id opened a turn: %d turns, want %d; status %s", len(rec.Turns), maxInteractiveDedup+1, rec.Status)
	}
}
