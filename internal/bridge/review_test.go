package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// A control command (interrupt, clear, compact) whose sender gave up must not
// survive for redelivery: a stale interrupt would abort unrelated later work,
// and a stale clear would wipe a context the caller no longer meant to wipe.
// Only deliver is worth redelivering after its sender stopped waiting.
func TestAbandonedControlCommandsAreDropped(t *testing.T) {
	for _, cmd := range []Command{Interrupt(), Clear(), Compact("focus")} {
		t.Run(cmd.Op+"/cancel", func(t *testing.T) {
			h := newTestHub(newFakeClock())
			s := mustConnect(t, h, agentA)
			ctx, cancel := context.WithCancel(context.Background())
			done := sendAsync(ctx, h, agentA, cmd)
			_ = mustNext(t, s)
			cancel()
			if err := result(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("Send err=%v, want context.Canceled", err)
			}
			if got := h.State(agentA).Pending; got != 0 {
				t.Fatalf("abandoned %s must leave the outbox, pending=%d", cmd.Op, got)
			}
			again := mustConnect(t, h, agentA)
			if got, err := again.Next(cancelledCtx()); err == nil {
				t.Fatalf("abandoned %s redelivered: %+v", cmd.Op, got)
			}
		})
		t.Run(cmd.Op+"/timeout", func(t *testing.T) {
			clock := newFakeClock()
			h := newTestHub(clock)
			_ = mustConnect(t, h, agentA)
			done := sendAsync(context.Background(), h, agentA, cmd)
			clock.waitArmed(t)
			clock.Advance(DefaultSlowAckTimeout)
			if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
				t.Fatalf("Send err=%v, want ErrAckTimeout", err)
			}
			if got := h.State(agentA).Pending; got != 0 {
				t.Fatalf("timed-out %s must leave the outbox, pending=%d", cmd.Op, got)
			}
		})
	}
}

// Deliver is the one op that stays queued after its sender stopped waiting:
// the mod deduplicates by id, so redelivery is safe and loses nothing.
func TestAbandonedDeliverStaysQueued(t *testing.T) {
	h := newTestHub(newFakeClock())
	ctx, cancel := context.WithCancel(context.Background())
	done := sendAsync(ctx, h, agentA, Deliver("keep me", false))
	if _, err := h.WaitFor(testCtx(t), agentA, func(s State) bool { return s.Pending == 1 }); err != nil {
		t.Fatalf("WaitFor pending: %v", err)
	}
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send err=%v, want context.Canceled", err)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("abandoned deliver must stay queued, pending=%d", got)
	}
}

// Compaction summarizes the conversation and can legitimately take minutes,
// and clear waits for idle first; both get the slow ack timeout.
func TestCompactAndClearUseSlowAckTimeout(t *testing.T) {
	for _, cmd := range []Command{Compact(""), Clear()} {
		t.Run(cmd.Op, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := newFakeClock()
				h := newTestHub(clock)
				_ = mustConnect(t, h, agentA)
				done := sendAsync(context.Background(), h, agentA, cmd)
				synctest.Wait()
				clock.Advance(DefaultAckTimeout)
				synctest.Wait()
				assertPending(t, done)
				clock.Advance(DefaultSlowAckTimeout - DefaultAckTimeout)
				if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
					t.Fatalf("Send err=%v, want ErrAckTimeout after the slow timeout", err)
				}
			})
		})
	}
}

func TestSlowAckTimeoutIsConfigurable(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock, func(o *Options) { o.SlowAckTimeout = 7 * time.Second })
	done := sendAsync(testCtx(t), h, agentA, Compact(""))
	clock.waitArmed(t)
	clock.Advance(7 * time.Second)
	if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
		t.Fatalf("Send err=%v, want ErrAckTimeout after the configured 7s", err)
	}
}

// The mod runs deliver, compact and clear one at a time, in order. A command
// queued behind an unacked one cannot be acked before it, so its clock waits
// until it reaches the head of the line, then starts in full.
func TestAckClockWaitsBehindEarlierSerialCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		compactDone := sendAsync(context.Background(), h, agentA, Compact(""))
		compact := mustNext(t, s)
		synctest.Wait()
		_ = armedCount(clock)

		deliverDone := sendAsync(context.Background(), h, agentA, Deliver("behind the compact", false))
		deliver := mustNext(t, s)
		synctest.Wait()
		if n := armedCount(clock); n != 0 {
			t.Fatalf("armed %d timers for a deliver queued behind a compact, want 0", n)
		}
		clock.Advance(4 * time.Minute) // a long compaction
		synctest.Wait()
		assertPending(t, deliverDone)

		ack(t, h, agentA, compact.ID)
		if err := result(t, compactDone); err != nil {
			t.Fatalf("compact Send: %v", err)
		}
		synctest.Wait()
		if n := armedCount(clock); n != 1 {
			t.Fatalf("armed %d timers once the deliver reached the head, want 1", n)
		}
		clock.Advance(DefaultAckTimeout - time.Second)
		synctest.Wait()
		assertPending(t, deliverDone)
		ack(t, h, agentA, deliver.ID)
		if err := result(t, deliverDone); err != nil {
			t.Fatalf("deliver Send: %v", err)
		}
	})
}

// Interrupt runs at once in the mod, so it neither waits behind serial
// commands nor holds them up.
func TestInterruptNeitherWaitsNorBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := newFakeClock()
		h := newTestHub(clock)
		_ = mustConnect(t, h, agentA)
		interruptDone := sendAsync(context.Background(), h, agentA, Interrupt())
		synctest.Wait()
		deliverDone := sendAsync(context.Background(), h, agentA, Deliver("after the interrupt", false))
		synctest.Wait()
		if n := armedCount(clock); n != 2 {
			t.Fatalf("armed %d timers, want 2 (interrupt and deliver both running)", n)
		}
		compactCtx, cancelCompact := context.WithCancel(context.Background())
		compactDone := sendAsync(compactCtx, h, agentA, Compact(""))
		synctest.Wait()
		if n := armedCount(clock); n != 0 {
			t.Fatalf("armed %d timers for a compact behind a deliver, want 0", n)
		}
		clock.Advance(DefaultAckTimeout)
		for name, done := range map[string]<-chan error{"interrupt": interruptDone, "deliver": deliverDone} {
			if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
				t.Fatalf("%s Send err=%v, want ErrAckTimeout", name, err)
			}
		}
		// The timed-out deliver stays queued, so the compact still waits.
		synctest.Wait()
		assertPending(t, compactDone)
		cancelCompact()
		if err := result(t, compactDone); !errors.Is(err, context.Canceled) {
			t.Fatalf("compact Send err=%v, want context.Canceled", err)
		}
	})
}

// A subscriber that is slow for one agent must not stall another agent's
// events: ordering is per agent, not global.
func TestSlowSubscriberStallsOnlyItsOwnAgent(t *testing.T) {
	const agentB = "leo-beta"
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	rec := &recorder{}
	sub := SubscriberFunc(func(ev Event) {
		if ev.Agent == agentA {
			once.Do(func() { close(entered) })
			<-release
		}
		rec.OnBridgeEvent(ev)
	})
	h := newTestHub(newFakeClock(), func(o *Options) { o.Subscriber = sub })
	mustOpen(t, h, agentB)
	go func() { _ = h.Apply(agentA, testLaunch, event(EventTurnStart)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber never saw agent A's event")
	}
	applied := make(chan error, 1)
	go func() { applied <- h.Apply(agentB, testLaunch, event(EventTurnStart)) }()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatalf("Apply B: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("agent B's event was stalled behind agent A's slow subscriber")
	}
	close(release)
}

// hello may carry the mod's own view of whether a turn is running. A fresh
// process resuming the same session says busy:false, which clears a busy
// flag left behind by a predecessor that died mid-turn.
func TestHelloBusyOverridesStaleTurnState(t *testing.T) {
	h := newTestHub(newFakeClock())
	_ = mustConnect(t, h, agentA)
	apply(t, h, agentA, hello("s-1"))
	apply(t, h, agentA, event(EventTurnStart))
	idle := false
	apply(t, h, agentA, Report{Type: ReportHello, SessionID: "s-1", ClaudeVersion: "2.1.289", Busy: &idle})
	if h.State(agentA).Busy {
		t.Fatal("hello busy:false must clear a stale busy flag for the same session")
	}
	busy := true
	apply(t, h, agentA, Report{Type: ReportHello, SessionID: "s-2", ClaudeVersion: "2.1.289", Busy: &busy})
	if !h.State(agentA).Busy {
		t.Fatal("hello busy:true must mark the agent busy")
	}
}
