package bridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A forgotten key is tombstoned: until its owner opens a new generation, no
// command can be queued for it and no late report from the launch that
// held it can bring its state back.
func TestForgottenKeyRefusesWorkUntilReopened(t *testing.T) {
	h := newTestHub(newFakeClock())
	if _, err := h.Open(agentA); err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.Forget(agentA)

	if _, err := h.Enqueue(agentA, Deliver("late", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Enqueue after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Send(testCtx(t), agentA, Deliver("late", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Send after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Apply(agentA, hello("s-old")); !errors.Is(err, ErrForgotten) {
		t.Fatalf("hello after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Apply(agentA, event(EventTurnStart)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("event after Forget: err=%v, want ErrForgotten", err)
	}
	if _, err := h.Connect(agentA); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Connect after Forget: err=%v, want ErrForgotten", err)
	}
	if st := h.State(agentA); st.Connected || st.SessionID != "" || !st.HelloAt.IsZero() || st.Busy || st.Pending != 0 {
		t.Fatalf("a forgotten key's state came back: %+v", st)
	}

	if _, err := h.Open(agentA); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := h.Enqueue(agentA, Deliver("fresh", true)); err != nil {
		t.Fatalf("Enqueue after reopen: %v", err)
	}
}

func TestForgetUnlessConnectedTombstones(t *testing.T) {
	h := newTestHub(newFakeClock())
	if !h.ForgetUnlessConnected(agentA) {
		t.Fatal("ForgetUnlessConnected = false for an unconnected key")
	}
	if err := h.Apply(agentA, hello("s-old")); !errors.Is(err, ErrForgotten) {
		t.Fatalf("hello after ForgetUnlessConnected: err=%v, want ErrForgotten", err)
	}
}

// A command routed to one generation never lands in the next: the
// successor launch must not receive what was meant for its predecessor.
func TestSendToRefusesAnEarlierGeneration(t *testing.T) {
	h := newTestHub(newFakeClock())
	first, err := h.Open(agentA)
	if err != nil {
		t.Fatal(err)
	}
	h.Forget(agentA)
	second, err := h.Open(agentA)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("reopening kept the generation: %+v", first)
	}
	if err := h.SendTo(testCtx(t), first, Deliver("for the predecessor", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("SendTo(old generation): err=%v, want ErrForgotten", err)
	}
	if _, err := h.EnqueueTo(first, Deliver("for the predecessor", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("EnqueueTo(old generation): err=%v, want ErrForgotten", err)
	}
	if got := h.State(agentA).Pending; got != 0 {
		t.Fatalf("successor has %d pending commands, want 0", got)
	}
	if _, err := h.EnqueueTo(second, Deliver("for the successor", true)); err != nil {
		t.Fatalf("EnqueueTo(current generation): %v", err)
	}
}

func TestLiveNamesTheConnectedGeneration(t *testing.T) {
	h := newTestHub(newFakeClock())
	opened, err := h.Open(agentA)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Live(agentA); ok {
		t.Fatal("Live before any connection")
	}
	_ = mustConnect(t, h, agentA)
	got, ok := h.Live(agentA)
	if !ok || got != opened {
		t.Fatalf("Live = %+v, %v; want %+v, true", got, ok, opened)
	}
	h.Forget(agentA)
	if _, ok := h.Live(agentA); ok {
		t.Fatal("Live after Forget")
	}
}

// After a daemon restart the surviving claude's mod can reconnect before
// the supervisor adopts its session; opening the key then keeps that
// stream rather than tearing it down.
func TestOpenKeepsALiveKeysState(t *testing.T) {
	h := newTestHub(newFakeClock())
	s := mustConnect(t, h, agentA)
	if _, err := h.Open(agentA); err != nil {
		t.Fatal(err)
	}
	if !h.Connected(agentA) {
		t.Fatal("Open dropped a live connection")
	}
	if _, err := h.Enqueue(agentA, Clear()); err != nil {
		t.Fatal(err)
	}
	if cmd := mustNext(t, s); cmd.Op != OpClear {
		t.Fatalf("stream got %+v", cmd)
	}
}

// A caller may name its command: the opening prompt's id is derived from
// its conversation and text, so re-queueing it after a relaunch or daemon
// restart reaches the mod's dedup as the same command.
func TestEnqueueToKeepsACallerID(t *testing.T) {
	h := newTestHub(newFakeClock())
	target, err := h.Open(agentA)
	if err != nil {
		t.Fatal(err)
	}
	cmd := Deliver("the opening", true)
	cmd.ID = "open-abc"
	first, err := h.EnqueueTo(target, cmd)
	if err != nil {
		t.Fatalf("EnqueueTo: %v", err)
	}
	if first.ID != "open-abc" {
		t.Fatalf("ticket id = %q, want the caller's", first.ID)
	}
	again, err := h.EnqueueTo(target, cmd)
	if err != nil {
		t.Fatalf("re-enqueue of the same command: %v", err)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("pending=%d after re-enqueueing the same command, want 1", got)
	}
	other := Deliver("something else", true)
	other.ID = "open-abc"
	if _, err := h.EnqueueTo(target, other); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("a different command under a queued id: err=%v, want ErrInvalidCommand", err)
	}

	s := mustConnect(t, h, agentA)
	if got := mustNext(t, s); got.ID != "open-abc" {
		t.Fatalf("streamed id %q, want open-abc", got.ID)
	}
	ack(t, h, agentA, "open-abc")
	for _, tk := range []*Ticket{first, again} {
		select {
		case <-tk.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("ticket never resolved on ack")
		}
		if err := tk.Err(); err != nil {
			t.Fatalf("ticket err = %v, want nil", err)
		}
	}
}

func TestTicketReportsRejectionAndForget(t *testing.T) {
	h := newTestHub(newFakeClock())
	target, _ := h.Open(agentA)
	rejected, err := h.EnqueueTo(target, Deliver("refused", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(agentA, Report{Type: ReportAck, ID: rejected.ID, OK: false, Error: "dropped"}); err != nil {
		t.Fatal(err)
	}
	<-rejected.Done()
	if !errors.Is(rejected.Err(), ErrRejected) {
		t.Fatalf("rejected ticket err = %v", rejected.Err())
	}

	forgotten, err := h.EnqueueTo(target, Deliver("orphaned", true))
	if err != nil {
		t.Fatal(err)
	}
	h.Forget(agentA)
	<-forgotten.Done()
	if !errors.Is(forgotten.Err(), ErrForgotten) {
		t.Fatalf("forgotten ticket err = %v", forgotten.Err())
	}
}

// A ticket's holder can stop watching without dropping the command.
func TestTicketWaitLeavesTheCommandQueued(t *testing.T) {
	h := newTestHub(newFakeClock())
	target, _ := h.Open(agentA)
	tk, err := h.EnqueueTo(target, Deliver("later", true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tk.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("pending=%d, want the command still queued", got)
	}
}

// Tombstones only have to outlive a dead launch's last reports, so they
// expire instead of accumulating one per finished dispatch.
func TestTombstonesExpire(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	h.Forget(agentA)
	clock.Advance(TombstoneTTL + time.Second)
	h.Forget("leo-other") // any forget sweeps expired tombstones

	h.mu.Lock()
	_, kept := h.lives[agentA]
	h.mu.Unlock()
	if kept {
		t.Fatal("an expired tombstone was kept")
	}
	if _, err := h.Enqueue(agentA, Clear()); err != nil {
		t.Fatalf("Enqueue after the tombstone expired: %v", err)
	}
}

// A launch that is over forgets only its own generation: a successor that
// already reopened the key (a stop and respawn racing the old loop) keeps
// its state.
func TestForgetGenSparesASuccessor(t *testing.T) {
	h := newTestHub(newFakeClock())
	old, _ := h.Open(agentA)
	h.Forget(agentA)
	successor, _ := h.Open(agentA)
	_ = mustConnect(t, h, agentA)

	if h.ForgetGen(old) {
		t.Fatal("ForgetGen forgot a generation that was already over")
	}
	if h.ForgetGenUnlessConnected(old) {
		t.Fatal("ForgetGenUnlessConnected forgot a generation that was already over")
	}
	if !h.Connected(agentA) {
		t.Fatal("the successor's stream was dropped")
	}
	if h.ForgetGenUnlessConnected(successor) {
		t.Fatal("ForgetGenUnlessConnected forgot a connected generation")
	}
	if !h.ForgetGen(successor) {
		t.Fatal("ForgetGen refused the current generation")
	}
	if _, err := h.Enqueue(agentA, Clear()); !errors.Is(err, ErrForgotten) {
		t.Fatalf("after ForgetGen: err=%v, want ErrForgotten", err)
	}
}

// ConnectedSince is the settle predicate: this generation's mod connected or
// said hello at or after since, whether or not the stream is up right now.
func TestStateConnectedSince(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	target, _ := h.Open(agentA)
	launched := clock.Now()
	if h.State(agentA).ConnectedSince(target, launched) {
		t.Fatal("connected before any connection")
	}
	clock.Advance(time.Second)
	s := mustConnect(t, h, agentA)
	s.Close()
	st := h.State(agentA)
	if st.Connected {
		t.Fatal("stream still connected after Close")
	}
	if !st.ConnectedSince(target, launched) {
		t.Fatalf("a mod that connected after the launch must count: %+v", st)
	}
	if st.ConnectedSince(target, clock.Now().Add(time.Second)) {
		t.Fatal("a connection before since counted")
	}
	if st.ConnectedSince(Target{Key: agentA, Gen: target.Gen + 1}, launched) {
		t.Fatal("another generation's connection counted")
	}
	if !st.ConnectedSince(target, time.Time{}) {
		t.Fatal("the zero since must accept any connection of the generation")
	}
}
