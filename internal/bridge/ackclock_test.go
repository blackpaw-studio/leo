package bridge

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// These tests run in a synctest bubble so synctest.Wait can tell when every
// goroutine (Send included) is parked; "no timer armed" and "still pending"
// are then exact checks, not timing guesses. Time itself is the injected
// fakeClock.

// assertPending fails if Send has already returned. Call after synctest.Wait.
func assertPending(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("Send returned early: %v", err)
	default:
	}
}

// armedCount drains and counts timers armed since the last call.
func armedCount(c *fakeClock) int {
	n := 0
	for {
		select {
		case <-c.armed:
			n++
		default:
			return n
		}
	}
}

// $.prompt.submit resolves only once Claude is idle, so a deliver sent
// mid-turn is acked after the turn ends: the ack clock must not run while a
// turn does, and starts fresh when the agent goes idle.
func TestAckClockPausesWhileBusyAndStartsOnIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))

		done := sendAsync(context.Background(), h, agentA, Deliver("queued behind the turn", false))
		_ = mustNext(t, s)
		synctest.Wait()
		if n := armedCount(clock); n != 0 {
			t.Fatalf("armed %d ack timers while a turn was running", n)
		}
		clock.Advance(10 * DefaultAckTimeout)
		synctest.Wait()
		assertPending(t, done)

		apply(t, h, agentA, event(EventTurnComplete))
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d ack timers on going idle, want 1", n)
		}
		clock.Advance(DefaultAckTimeout - time.Second)
		synctest.Wait()
		assertPending(t, done)
		clock.Advance(time.Second)
		if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
			t.Fatalf("Send err=%v, want ErrAckTimeout a full timeout after going idle", err)
		}
	})
}

func TestAckClockAckAfterLongTurnSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))
		done := sendAsync(context.Background(), h, agentA, Deliver("hi", false))
		cmd := mustNext(t, s)
		synctest.Wait()
		clock.Advance(time.Hour) // a long turn
		apply(t, h, agentA, event(EventTurnComplete))
		ack(t, h, agentA, cmd.ID) // submit resolves once idle
		if err := result(t, done); err != nil {
			t.Fatalf("Send: %v", err)
		}
	})
}

// Each return to idle restarts the clock from zero; it does not resume the
// remainder of an earlier idle stretch.
func TestAckClockRestartsOnEachIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		_ = mustConnect(t, h, agentA)
		done := sendAsync(context.Background(), h, agentA, Compact(""))
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers for an idle agent, want 1", n)
		}
		clock.Advance(DefaultAckTimeout - time.Second)

		apply(t, h, agentA, event(EventTurnStart))
		synctest.Wait()
		clock.Advance(time.Hour)
		synctest.Wait()
		assertPending(t, done)

		apply(t, h, agentA, event(EventTurnComplete))
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers on the second idle, want a fresh one", n)
		}
		clock.Advance(DefaultAckTimeout - time.Second)
		synctest.Wait()
		assertPending(t, done)
		clock.Advance(time.Second)
		if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
			t.Fatalf("Send err=%v, want ErrAckTimeout", err)
		}
	})
}

// A turn that ends and a new one that starts between two looks at the
// state still counts as going idle: the clock restarts rather than keeping
// the deadline from before.
func TestAckClockRestartsOnIdleFlipItDidNotWitness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		_ = mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))
		apply(t, h, agentA, event(EventTurnComplete))
		done := sendAsync(context.Background(), h, agentA, Clear())
		synctest.Wait()
		_ = armedCount(clock)
		clock.Advance(DefaultAckTimeout - time.Second)

		// Busy then idle again, applied under the hub's event lock so Send
		// cannot observe the busy moment in between.
		h.eventMu.Lock()
		h.mu.Lock()
		h.recordLocked(agentA, h.agents[agentA], event(EventTurnStart))
		h.recordLocked(agentA, h.agents[agentA], event(EventTurnComplete))
		h.notifyLocked()
		h.mu.Unlock()
		h.eventMu.Unlock()
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers after an unwitnessed busy/idle flip, want 1", n)
		}
		clock.Advance(time.Second)
		synctest.Wait()
		assertPending(t, done)
		clock.Advance(DefaultAckTimeout)
		if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
			t.Fatalf("Send err=%v, want ErrAckTimeout from the restarted clock", err)
		}
	})
}

// Busy is only trustworthy while the mod is connected: a mod that vanished
// mid-turn will never report turn.complete, so the clock runs.
func TestAckClockRunsWhenDisconnectedMidTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))
		done := sendAsync(context.Background(), h, agentA, Deliver("hi", true))
		synctest.Wait()
		s.Close()
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers after the stream dropped mid-turn, want 1", n)
		}
		clock.Advance(DefaultAckTimeout)
		if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
			t.Fatalf("Send err=%v, want ErrAckTimeout", err)
		}
		if got := h.State(agentA).Pending; got != 1 {
			t.Fatalf("timed-out command must stay queued, pending=%d", got)
		}
	})
}

// The mod runs interrupt at once, ahead of a deliver still waiting for idle:
// interrupt's clock ignores busy, the stream does not hold it behind the
// unacked deliver, and acks may arrive out of order.
func TestInterruptJumpsAheadOfWaitingDeliver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))

		deliverDone := sendAsync(context.Background(), h, agentA, Deliver("next task", false))
		deliver := mustNext(t, s)
		synctest.Wait()
		interruptDone := sendAsync(context.Background(), h, agentA, Interrupt())
		interrupt := mustNext(t, s) // written without waiting for deliver's ack
		if interrupt.Op != OpInterrupt {
			t.Fatalf("second command = %+v, want the interrupt", interrupt)
		}
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers, want 1 (the interrupt's, despite busy)", n)
		}

		ack(t, h, agentA, interrupt.ID)
		if err := result(t, interruptDone); err != nil {
			t.Fatalf("interrupt Send: %v", err)
		}
		synctest.Wait()
		assertPending(t, deliverDone)

		apply(t, h, agentA, event(EventTurnComplete))
		ack(t, h, agentA, deliver.ID)
		if err := result(t, deliverDone); err != nil {
			t.Fatalf("deliver Send: %v", err)
		}
	})
}

func TestInterruptAckTimesOutEvenWhileBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		_ = mustConnect(t, h, agentA)
		apply(t, h, agentA, event(EventTurnStart))
		done := sendAsync(context.Background(), h, agentA, Interrupt())
		synctest.Wait()
		clock.Advance(DefaultAckTimeout)
		if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
			t.Fatalf("interrupt Send err=%v, want ErrAckTimeout while busy", err)
		}
	})
}

// After /clear the mod says hello again with the new session id on the same
// stream. That is a metadata update: no redelivery, same connection.
func TestRepeatHelloOnLiveStreamIsMetadataOnly(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	s := mustConnect(t, h, agentA)
	connectedAt := clock.Now()
	apply(t, h, agentA, hello("s-1"))
	inflight, _ := h.Enqueue(agentA, Deliver("in flight", true))
	if got := mustNext(t, s); got.ID != inflight {
		t.Fatalf("got %q, want %q", got.ID, inflight)
	}

	clock.Advance(time.Minute)
	apply(t, h, agentA, hello("s-2"))

	if got, err := s.Next(cancelledCtx()); err == nil {
		t.Fatalf("repeat hello redelivered %+v", got)
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("repeat hello ended the stream: %v", err)
	}
	st := h.State(agentA)
	if !st.Connected || !st.ConnectedAt.Equal(connectedAt) || st.SessionID != "s-2" || st.Pending != 1 {
		t.Fatalf("state after repeat hello = %+v; want same connection, session s-2, 1 pending", st)
	}
	next, _ := h.Enqueue(agentA, Clear())
	if got := mustNext(t, s); got.ID != next {
		t.Fatalf("stream got %q after repeat hello, want only the new command %q", got.ID, next)
	}
}
