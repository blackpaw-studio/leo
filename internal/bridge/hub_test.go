package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const agentA = "leo-alpha"

func newTestHub(clock Clock, opts ...func(*Options)) *Hub {
	o := Options{Clock: clock, NewID: seqIDs()}
	for _, fn := range opts {
		fn(&o)
	}
	return New(o)
}

func TestEnqueueValidates(t *testing.T) {
	h := newTestHub(newFakeClock())
	if _, err := h.Enqueue("", Clear()); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("empty agent: err=%v, want ErrInvalidAgent", err)
	}
	if _, err := h.Enqueue(agentA, Deliver("", true)); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty deliver: err=%v, want ErrInvalidCommand", err)
	}
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("rejected commands must not be queued, pending=%d", got)
	}
}

func TestEnqueueRejectsDuplicateID(t *testing.T) {
	h := New(Options{Clock: newFakeClock(), NewID: func() string { return "same" }})
	if _, err := h.Enqueue(agentA, Clear()); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if _, err := h.Enqueue(agentA, Clear()); err == nil {
		t.Fatal("a duplicate id would make acks ambiguous; want an error")
	}
}

func TestOutboxCapRefusesOverflow(t *testing.T) {
	h := newTestHub(newFakeClock(), func(o *Options) { o.MaxPending = 2 })
	for i := 0; i < 2; i++ {
		if _, err := h.Enqueue(agentA, Clear()); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if _, err := h.Enqueue(agentA, Clear()); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("over cap: err=%v, want ErrOutboxFull", err)
	}
}

func TestQueuedBeforeConnectStreamsInOrder(t *testing.T) {
	h := newTestHub(newFakeClock())
	var ids []string
	for _, cmd := range []Command{Deliver("one", true), Compact("two"), Interrupt()} {
		id, err := h.Enqueue(agentA, cmd)
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		ids = append(ids, id)
	}
	s := mustConnect(t, h, agentA)
	for i, want := range ids {
		got := mustNext(t, s)
		if got.ID != want {
			t.Fatalf("command %d: id=%q, want %q", i, got.ID, want)
		}
	}
	if got, err := s.Next(cancelledCtx()); err == nil {
		t.Fatalf("unexpected extra command %+v", got)
	}
}

// cancelledCtx makes Next a non-blocking probe: it still returns a command
// that is ready, but reports the context error instead of waiting.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestStreamWakesForCommandsEnqueuedAfterConnect(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	got := make(chan Command, 1)
	go func() {
		cmd, _ := s.Next(testCtx(t))
		got <- cmd
	}()
	id, err := h.Enqueue(agentA, Deliver("late", false))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case cmd := <-got:
		if cmd.ID != id || cmd.Text != "late" || cmd.AsUser {
			t.Fatalf("got %+v, want id %q text late as_user false", cmd, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked stream never woke for a new command")
	}
}

func TestReconnectRedeliversUnackedInOrder(t *testing.T) {
	h := newTestHub(newFakeClock())
	a, _ := h.Enqueue(agentA, Deliver("a", true))
	b, _ := h.Enqueue(agentA, Deliver("b", true))
	c, _ := h.Enqueue(agentA, Deliver("c", true))

	first := mustConnect(t, h, agentA)
	if got := mustNext(t, first).ID; got != a {
		t.Fatalf("first stream: got %q, want %q", got, a)
	}
	if got := mustNext(t, first).ID; got != b {
		t.Fatalf("first stream: got %q, want %q", got, b)
	}
	ack(t, h, agentA, a)

	second := mustConnect(t, h, agentA)
	if _, err := first.Next(testCtx(t)); !errors.Is(err, ErrStreamReplaced) {
		t.Fatalf("replaced stream Next err=%v, want ErrStreamReplaced", err)
	}
	for _, want := range []string{b, c} {
		if got := mustNext(t, second).ID; got != want {
			t.Fatalf("second stream: got %q, want %q (unacked commands must be resent in order)", got, want)
		}
	}
}

func TestReplaceEndsBlockedStream(t *testing.T) {
	h := newTestHub(newFakeClock())
	first := mustConnect(t, h, agentA)
	ended := make(chan error, 1)
	go func() {
		_, err := first.Next(context.Background())
		ended <- err
	}()

	second := mustConnect(t, h, agentA)
	if err := result(t, ended); !errors.Is(err, ErrStreamReplaced) {
		t.Fatalf("blocked Next err=%v, want ErrStreamReplaced", err)
	}
	// The old handler's deferred Close must not unregister its successor.
	first.Close()
	if !h.Connected(agentA) {
		t.Fatal("closing a replaced stream unregistered the live one")
	}
	second.Close()
	if h.Connected(agentA) {
		t.Fatal("closing the live stream must unregister it")
	}
}

func TestReplacedStreamNeverEmitsQueuedCommands(t *testing.T) {
	h := newTestHub(newFakeClock())
	first := mustConnect(t, h, agentA)
	_ = mustConnect(t, h, agentA)
	if _, err := h.Enqueue(agentA, Clear()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if cmd, err := first.Next(cancelledCtx()); !errors.Is(err, ErrStreamReplaced) {
		t.Fatalf("replaced stream Next = %+v, %v; want ErrStreamReplaced", cmd, err)
	}
}

func TestStreamCloseUnregisters(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	if !h.Connected(agentA) {
		t.Fatal("Connected=false after Connect")
	}
	s.Close()
	s.Close() // idempotent
	if h.Connected(agentA) {
		t.Fatal("Connected=true after Close")
	}
	if _, err := s.Next(testCtx(t)); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next after Close err=%v, want ErrStreamClosed", err)
	}
}

func TestNextHonorsContext(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		_, err := s.Next(ctx)
		ended <- err
	}()
	cancel()
	if err := result(t, ended); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next err=%v, want context.Canceled", err)
	}
}

func TestSendReturnsOnAck(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Deliver("hi", true))
	cmd := mustNext(t, s)
	ack(t, h, agentA, cmd.ID)
	if err := result(t, done); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("acked command still pending (%d)", got)
	}
}

func TestSendAckTimeoutKeepsCommandForRedelivery(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Deliver("slow", true))
	sent := mustNext(t, s)
	clock.waitArmed(t)
	clock.Advance(DefaultAckTimeout)

	err := result(t, done)
	if !errors.Is(err, ErrAckTimeout) {
		t.Fatalf("Send err=%v, want ErrAckTimeout", err)
	}
	if !strings.Contains(err.Error(), sent.ID) {
		t.Fatalf("timeout error %q should name command %s", err, sent.ID)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("timed-out command must stay queued, pending=%d", got)
	}
	again := mustConnect(t, h, agentA)
	if got := mustNext(t, again); got.ID != sent.ID || got.Text != "slow" {
		t.Fatalf("redelivered %+v, want the timed-out command %s", got, sent.ID)
	}
}

func TestSendAckTimeoutIsConfigurable(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock, func(o *Options) { o.AckTimeout = 5 * time.Second })
	done := sendAsync(testCtx(t), h, agentA, Deliver("hi", false))
	clock.waitArmed(t)
	clock.Advance(5 * time.Second)
	if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
		t.Fatalf("Send err=%v, want ErrAckTimeout after the configured 5s", err)
	}
}

func TestLateAckAfterTimeoutRemovesCommand(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Deliver("late", false))
	cmd := mustNext(t, s)
	clock.waitArmed(t)
	clock.Advance(DefaultAckTimeout)
	if err := result(t, done); !errors.Is(err, ErrAckTimeout) {
		t.Fatalf("Send err=%v, want ErrAckTimeout", err)
	}
	ack(t, h, agentA, cmd.ID)
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("late ack must still settle the command, pending=%d", got)
	}
	again := mustConnect(t, h, agentA)
	if got, err := again.Next(cancelledCtx()); err == nil {
		t.Fatalf("settled command redelivered: %+v", got)
	}
}

// When the ack lands between the timeout firing and Send giving up, the ack
// wins: the command did run, so reporting a timeout would invite a duplicate
// retry. Holding the hub lock across both makes that window certain — Send
// wakes on the timer, then must take the lock, and the ack is already in.
func TestSendPrefersAckRacingTimeout(t *testing.T) {
	for i := 0; i < 50; i++ {
		clock := newFakeClock()
		h := newTestHub(clock)
		s := mustConnect(t, h, agentA)
		done := sendAsync(testCtx(t), h, agentA, Deliver("racing", false))
		cmd := mustNext(t, s)
		clock.waitArmed(t)
		h.mu.Lock()
		clock.Advance(DefaultAckTimeout)
		h.ackLocked(agentA, cmd.ID, true, "")
		h.mu.Unlock()
		if err := result(t, done); err != nil {
			t.Fatalf("iteration %d: Send err=%v, want nil (acked before the timeout was observed)", i, err)
		}
	}
}

func TestSendRejectedAckDropsCommand(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Compact(""))
	cmd := mustNext(t, s)
	if err := h.Apply(agentA, Report{Type: ReportAck, ID: cmd.ID, OK: false, Error: "turn running"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	err := result(t, done)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "turn running") {
		t.Fatalf("Send err=%v, want ErrRejected carrying the mod's message", err)
	}
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("rejected command must be dropped, pending=%d", got)
	}
}

func TestSendContextCancelKeepsCommand(t *testing.T) {
	h := newTestHub(newFakeClock())
	ctx, cancel := context.WithCancel(context.Background())
	done := sendAsync(ctx, h, agentA, Deliver("cancelled", false))
	// Wait until Send has queued the command before cancelling.
	if _, err := h.WaitFor(testCtx(t), agentA, func(s State) bool { return s.Pending == 1 }); err != nil {
		t.Fatalf("WaitFor pending: %v", err)
	}
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send err=%v, want context.Canceled", err)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("cancelled send must leave the command for redelivery, pending=%d", got)
	}
}

func TestSendSurvivesStreamReplacement(t *testing.T) {
	h := newTestHub(newFakeClock())
	first := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Deliver("survive", true))
	sent := mustNext(t, first)
	second := mustConnect(t, h, agentA)
	resent := mustNext(t, second)
	if resent.ID != sent.ID {
		t.Fatalf("replacement stream got %q, want redelivery of %q", resent.ID, sent.ID)
	}
	ack(t, h, agentA, resent.ID)
	if err := result(t, done); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestAckForUnknownIDIsIgnored(t *testing.T) {
	h := newTestHub(newFakeClock())
	if err := h.Apply(agentA, Report{Type: ReportAck, ID: "nope", OK: true}); err != nil {
		t.Fatalf("unknown ack must be accepted and ignored, got %v", err)
	}
}

func TestAckIsScopedToItsAgent(t *testing.T) {
	h := newTestHub(newFakeClock())
	id, _ := h.Enqueue(agentA, Clear())
	ack(t, h, "leo-beta", id)
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("another agent's ack settled %s, pending=%d", id, got)
	}
}

func TestForgetDropsStateAndWakesSenders(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Clear())
	_ = mustNext(t, s)
	h.Forget(agentA)
	if err := result(t, done); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Send err=%v, want ErrForgotten", err)
	}
	if _, err := s.Next(testCtx(t)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("stream Next err=%v, want ErrForgotten", err)
	}
	if st := h.State(agentA); st.Connected || st.Pending != 0 {
		t.Fatalf("forgotten agent still has state: %+v", st)
	}
}

func TestCloseEndsStreamsAndRefusesWork(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	done := sendAsync(testCtx(t), h, agentA, Clear())
	_ = mustNext(t, s)
	h.Close()
	h.Close() // idempotent
	if err := result(t, done); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send err=%v, want ErrClosed", err)
	}
	if _, err := s.Next(testCtx(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("stream Next err=%v, want ErrClosed", err)
	}
	if _, err := h.Connect(agentA); !errors.Is(err, ErrClosed) {
		t.Fatalf("Connect err=%v, want ErrClosed", err)
	}
	if _, err := h.Enqueue(agentA, Clear()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Enqueue err=%v, want ErrClosed", err)
	}
}

func TestWaitForConnect(t *testing.T) {
	h := newTestHub(newFakeClock())
	got := make(chan State, 1)
	go func() {
		st, err := h.WaitFor(testCtx(t), agentA, func(s State) bool { return s.Connected })
		if err == nil {
			got <- st
		}
		close(got)
	}()
	_ = mustConnect(t, h, agentA)
	select {
	case st, ok := <-got:
		if !ok || !st.Connected {
			t.Fatalf("WaitFor returned %+v ok=%v, want a connected state", st, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitFor never observed the connection")
	}
}

func TestWaitForHonorsContext(t *testing.T) {
	h := newTestHub(newFakeClock())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.WaitFor(ctx, agentA, func(State) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitFor err=%v, want context.Canceled", err)
	}
}

// A "mod" that keeps reconnecting while many senders race it. Every Send
// must still complete exactly once with success; run under -race.
func TestConcurrentSendsAcrossReconnects(t *testing.T) {
	const senders = 40
	h := New(Options{Clock: newFakeClock()}) // default random ids
	ctx := testCtx(t)
	modCtx, stopMod := context.WithCancel(ctx)
	var modWG sync.WaitGroup
	modWG.Add(1)
	go func() {
		defer modWG.Done()
		for round := 0; modCtx.Err() == nil; round++ {
			s, err := h.Connect(agentA)
			if err != nil {
				return
			}
			// Read a few commands per connection, then reconnect. Every
			// other command is "lost to a reload": left unacked, followed
			// by an immediate reconnect that must redeliver it.
			for i := 0; i < 3; i++ {
				cmd, err := s.Next(modCtx)
				if err != nil {
					break
				}
				if (round+i)%2 != 0 {
					break
				}
				_ = h.Apply(agentA, Report{Type: ReportAck, ID: cmd.ID, OK: true})
			}
			// Deliberately not s.Close(): a reload's old stream lingers
			// until the replacement Connect ends it.
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.Send(ctx, agentA, Deliver("msg", false))
		}()
	}
	wg.Wait()
	stopMod()
	modWG.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("pending=%d after every send was acked", got)
	}
}
