package consult

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// A Stop under an id no turn answers to may end a turn this run never saw
// start (a prompt a person queued under the previous turn, run after it).
// While a sent turn waits for its submit, that Stop is held, not adopted:
// the sent turn's own submit, under another id, may still arrive.
func TestPromptIDUnknownStopDoesNotAdoptAnArmedTurnWhoseSubmitFollowsUnderAnotherID(t *testing.T) {
	d, _, id, now := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening"))
	sent, err := d.Send(context.Background(), id, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	// B, queued by a person under the opening, ends: its Stop beats the sent turn's submit.
	reportAll(t, d, id, idStop(t, "s2", "b", "B's output"))
	if got := turnByID(idleRecord(t, d, id), sent.TurnID); got.Outcome != "" {
		t.Fatalf("an unknown stop closed the armed turn at once: %+v", got)
	}
	reportAll(t, d, id, idSubmit(t, "u2", "z", "follow up"))
	advanceClock(now, 2*unmatchedGrace)
	d.Sweep(*now)
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != "" || !got.Delivered || rec.Status != StatusRunning {
		t.Fatalf("B's stop was adopted by the sent turn: %+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s3", "z", "follow up done"))
	rec = idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != TurnFinished || got.Text != "follow up done" || rec.Status != StatusIdle {
		t.Fatalf("sent turn did not close on its own stop: %+v", rec)
	}
}

// The submit may also beat the stranger's Stop.
func TestPromptIDUnknownStopAfterTheArmedTurnsSubmitClosesNothing(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening"))
	sent, err := d.Send(context.Background(), id, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, idSubmit(t, "u2", "z", "follow up"), idStop(t, "s2", "b", "B's output"))
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != "" || rec.Status != StatusRunning {
		t.Fatalf("a stranger's stop closed the sent turn: %+v", rec)
	}
}

// With no submit in sight the stop is the armed turn's after all (its
// submit hook was lost), but only once the grace for that submit has run out.
func TestPromptIDUnknownStopAdoptsTheArmedTurnOnceNoSubmitCameInTheGrace(t *testing.T) {
	d, _, id, now := startArmedClaude(t)
	reportAll(t, d, id, idStop(t, "s1", "a", "done"))
	if rec := idleRecord(t, d, id); rec.Turns[0].Outcome != "" {
		t.Fatalf("closed before the grace ran out: %+v", rec)
	}
	advanceClock(now, unmatchedGrace-time.Second)
	d.Sweep(*now)
	if rec := idleRecord(t, d, id); rec.Turns[0].Outcome != "" {
		t.Fatalf("closed inside the grace: %+v", rec)
	}
	advanceClock(now, 2*time.Second)
	d.Sweep(*now)
	rec := idleRecord(t, d, id)
	if got := rec.Turns[0]; !got.Delivered || got.Outcome != TurnFinished || got.Text != "done" || rec.Status != StatusIdle {
		t.Fatalf("lost submit did not close the armed turn: %+v", rec)
	}
}

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
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "I'll wait for the build.\n\nBuild passed." || rec.Status != StatusIdle {
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

// A submit that names the held Stop's own id belongs to it, whenever it
// arrives: the grace only bounds how long a Stop waits to be claimed, and a
// submit past it but ahead of the Sweep still finds the Stop waiting.
func TestPromptIDHeldStopIsAppliedWhenItsOwnSubmitComes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
	}{
		{"before the grace ran out", unmatchedGrace - time.Second},
		{"at the grace, before the sweep", unmatchedGrace},
		{"long after the grace, before the sweep", 3 * unmatchedGrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, id, now := startArmedClaude(t)
			reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening"))
			sent, err := d.Send(context.Background(), id, "follow up")
			if err != nil {
				t.Fatal(err)
			}
			reportAll(t, d, id, idStop(t, "s2", "y", "its result"))
			advanceClock(now, tc.delay)
			reportAll(t, d, id, idSubmit(t, "u2", "y", "follow up"))
			d.Sweep(*now)
			rec := idleRecord(t, d, id)
			if got := turnByID(rec, sent.TurnID); got.Outcome != TurnFinished || got.Text != "its result" || rec.Status != StatusIdle {
				t.Fatalf("the held stop was not applied to its turn: %+v", rec)
			}
		})
	}
}

// A submit under another id, past the grace but ahead of the Sweep, delivers
// the armed turn: the held Stop was somebody else's and the turn waits for
// its own.
func TestPromptIDHeldStopIsDroppedWhenAnotherIDSubmitDeliversTheArmedTurn(t *testing.T) {
	d, _, id, now := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening"))
	sent, err := d.Send(context.Background(), id, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, idStop(t, "s2", "y", "B's output"))
	advanceClock(now, 2*unmatchedGrace)
	reportAll(t, d, id, idSubmit(t, "u2", "z", "follow up"))
	d.Sweep(*now)
	rec := idleRecord(t, d, id)
	if got := turnByID(rec, sent.TurnID); got.Outcome != "" || !got.Delivered {
		t.Fatalf("the sent turn did not wait for its own stop: %+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s3", "z", "follow up done"))
	if got := turnByID(idleRecord(t, d, id), sent.TurnID); got.Outcome != TurnFinished || got.Text != "follow up done" {
		t.Fatalf("sent turn = %+v", got)
	}
}

// Pending work belongs to the turn whose Stop carried it. A turn of leo's
// own that stops with nothing pending (a stale-stamped start) leaves the
// waiting turn waiting, and the wake still finds it.
func TestBridgedTurnStoppingWithNothingPendingLeavesTheWaitingTurnWaiting(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	waiting, err := d.Send(context.Background(), id, "build it")
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "bridge_turn_id": "t2", "last_assistant_message": "waiting", "background_tasks": shellAndMonitor["background_tasks"], "session_crons": []any{}})
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "build it", rt.lastCommandID()), HookReport{EventID: "e2", Payload: pending})
	reportAll(t, d, id, bridgeStart(t, "e3", "t3", "stale", "cmd-nobody-holds"), bridgeStop(t, "e4", "t3", "stale done"))
	rec := idleRecord(t, d, id)
	if w := turnByID(rec, waiting.TurnID); w.Outcome != "" || rec.Status != StatusWaiting || rec.PendingWork == nil {
		t.Fatalf("the stale turn's stop ended the wait: status=%s pending=%v turn=%+v", rec.Status, rec.PendingWork, w)
	}
	reportAll(t, d, id, bridgeWake(t, "e5", "t4", taskNotification), bridgeStop(t, "e6", "t4", "Build passed."))
	rec = idleRecord(t, d, id)
	if w := turnByID(rec, waiting.TurnID); w.Outcome != TurnFinished || w.Text != "waiting\n\nBuild passed." || rec.Status != StatusIdle || rec.PendingWork != nil || len(rec.Turns) != 3 {
		t.Fatalf("the wake did not finish the waiting turn: %+v", rec)
	}
	if d.slots.InUse() != 0 {
		t.Fatalf("slots in use = %d", d.slots.InUse())
	}
}

// A turn that stops is the only turn whose pending work its Stop settles:
// another turn's wait, and the run's waiting status, stay.
func TestPendingWorkIsOnlyClearedByItsOwnTurnsStop(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = sharedClock(&now)
	s := &runState{record: Record{ID: "d-two", Harness: "claude", Mode: ModeInteractive, Status: StatusRunning}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}}
	d.mu.Lock()
	d.runs[s.record.ID] = s
	waitingID := d.openTurnLocked(s, TurnSourceUser, "", false).TurnID
	d.bindKeyLocked(s, waitingID, "p:a")
	otherID := d.openTurnLocked(s, TurnSourceUser, "", false).TurnID
	d.bindKeyLocked(s, otherID, "p:h")
	d.waitOnBackgroundLocked(s, pendingWorkFromStop(shellAndMonitor), "", &s.record.Turns[turnIndexByID(s.record.Turns, waitingID)])
	d.mu.Unlock()

	reportAll(t, d, s.record.ID, idStop(t, "s1", "h", "human done"))
	rec := idleRecord(t, d, s.record.ID)
	if w := turnByID(rec, waitingID); w.Outcome != "" || rec.Status != StatusWaiting || rec.PendingWork == nil {
		t.Fatalf("another turn's stop ended the wait: status=%s pending=%v", rec.Status, rec.PendingWork)
	}
	if o := turnByID(rec, otherID); o.Outcome != TurnFinished {
		t.Fatalf("the stopped turn = %+v", o)
	}
	reportAll(t, d, s.record.ID, idStop(t, "s2", "a", "all done"))
	if rec = idleRecord(t, d, s.record.ID); rec.Status != StatusIdle || rec.PendingWork != nil {
		t.Fatalf("its own stop did not settle the wait: %+v", rec)
	}
}
