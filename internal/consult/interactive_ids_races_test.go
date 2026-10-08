package consult

import "testing"

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
