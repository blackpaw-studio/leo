package bridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// streamHolds reports whether s has nothing to hand out right now.
func streamHolds(s *Stream) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := s.Next(ctx)
	return errors.Is(err, context.DeadlineExceeded)
}

// Nothing queued behind a gate reaches the mod until the gate is acked ok:
// a launch's opening, whose refusal abandons the launch, must not have let
// anything after it run. A reconnect hands the gate out again and still
// holds the rest.
func TestNothingStreamsPastAnUnackedGate(t *testing.T) {
	h := newTestHub(newFakeClock())
	target := mustOpen(t, h, agentA)
	gate, err := h.EnqueueGate(target, Deliver("the opening", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.EnqueueTo(target, Deliver("carried", false)); err != nil {
		t.Fatal(err)
	}
	s := mustConnect(t, h, agentA)
	if got := mustNext(t, s); got.ID != gate.ID {
		t.Fatalf("first out %+v, want the gate", got)
	}
	if !streamHolds(s) {
		t.Fatal("a command streamed past the unacked gate")
	}
	again := mustConnect(t, h, agentA)
	if got := mustNext(t, again); got.ID != gate.ID {
		t.Fatalf("a reconnect handed out %+v first, want the gate", got)
	}
	if !streamHolds(again) {
		t.Fatal("a reconnect streamed past the unacked gate")
	}
	ack(t, h, agentA, gate.ID)
	if got := mustNext(t, again); got.Text != "carried" {
		t.Fatalf("after the gate's ack: %+v, want what it held back", got)
	}
}

// A refused gate seals its generation: nothing behind it ever streams, so
// the launch can be abandoned with all of it still undelivered.
func TestARefusedGateSealsItsGeneration(t *testing.T) {
	h := newTestHub(newFakeClock())
	target := mustOpen(t, h, agentA)
	gate, err := h.EnqueueGate(target, Deliver("the opening", true))
	if err != nil {
		t.Fatal(err)
	}
	behind, err := h.EnqueueTo(target, Deliver("carried", false))
	if err != nil {
		t.Fatal(err)
	}
	s := mustConnect(t, h, agentA)
	mustNext(t, s)
	if err := h.Apply(agentA, testLaunch, Report{Type: ReportAck, ID: gate.ID, OK: false, Error: "blocked"}); err != nil {
		t.Fatal(err)
	}
	if err := gate.Wait(testCtx(t)); !errors.Is(err, ErrRejected) {
		t.Fatalf("gate err=%v, want ErrRejected", err)
	}
	if !streamHolds(s) {
		t.Fatal("a command streamed past a refused gate")
	}
	again := mustConnect(t, h, agentA)
	if !streamHolds(again) {
		t.Fatal("a reconnect streamed past a refused gate")
	}
	if _, err := h.EnqueueTo(target, Deliver("later", false)); err != nil {
		t.Fatal(err)
	}
	if !streamHolds(again) {
		t.Fatal("a command queued after the refusal streamed")
	}
	select {
	case <-behind.Done():
		t.Fatalf("the command behind the gate settled (%v); it must stay queued for the caller", behind.Err())
	default:
	}
}

// Await waits on a queued command as Send does: its ack clock stands
// still while an earlier command is unacked, and an ack timeout leaves a
// deliver queued.
func TestAwaitTimesOutLikeSend(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	target := mustOpen(t, h, agentA)
	ticket, err := h.EnqueueTo(target, Deliver("slow", true))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Await(testCtx(t), ticket) }()
	clock.waitArmed(t)
	clock.Advance(DefaultAckTimeout)
	if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
		t.Fatalf("Await err=%v, want ErrAckTimeout", err)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("pending=%d: a timed-out deliver must stay queued", got)
	}
	go func() { done <- h.Await(testCtx(t), ticket) }()
	ack(t, h, agentA, ticket.ID)
	if err := result(t, done); err != nil {
		t.Fatalf("Await after the ack: %v", err)
	}
}
