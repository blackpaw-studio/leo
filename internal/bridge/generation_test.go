package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A forgotten key is tombstoned: until its owner opens a new generation, no
// command can be queued for it and no late report from the launch that
// held it can bring its state back.
func TestForgottenKeyRefusesWorkUntilReopened(t *testing.T) {
	h := newTestHub(newFakeClock())
	if _, err := h.Open(agentA, testLaunch); err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.Forget(agentA)

	if _, err := h.Enqueue(agentA, Deliver("late", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Enqueue after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Send(testCtx(t), agentA, Deliver("late", true)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Send after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Apply(agentA, testLaunch, hello("s-old")); !errors.Is(err, ErrForgotten) {
		t.Fatalf("hello after Forget: err=%v, want ErrForgotten", err)
	}
	if err := h.Apply(agentA, testLaunch, event(EventTurnStart)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("event after Forget: err=%v, want ErrForgotten", err)
	}
	if _, err := h.Connect(agentA, testLaunch); !errors.Is(err, ErrForgotten) {
		t.Fatalf("Connect after Forget: err=%v, want ErrForgotten", err)
	}
	if st := h.State(agentA); st.Connected || st.SessionID != "" || !st.HelloAt.IsZero() || st.Busy || st.Pending != 0 {
		t.Fatalf("a forgotten key's state came back: %+v", st)
	}

	if _, err := h.Open(agentA, testLaunch); err != nil {
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
	if err := h.Apply(agentA, testLaunch, hello("s-old")); !errors.Is(err, ErrForgotten) {
		t.Fatalf("hello after ForgetUnlessConnected: err=%v, want ErrForgotten", err)
	}
}

// A command routed to one generation never lands in the next: the
// successor launch must not receive what was meant for its predecessor.
func TestSendToRefusesAnEarlierGeneration(t *testing.T) {
	h := newTestHub(newFakeClock())
	first, err := h.Open(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	h.Forget(agentA)
	second, err := h.Open(agentA, testLaunch)
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
	opened, err := h.Open(agentA, testLaunch)
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

// Every Open starts a new generation bound to its launch: whatever the
// key's previous launch held (its stream, queued commands, turn state) is
// over, so a predecessor can never keep the successor's stream or opening.
func TestOpenStartsANewGenerationEveryTime(t *testing.T) {
	h := newTestHub(newFakeClock())
	first, err := h.Open(agentA, "launch-1")
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.Connect(agentA, "launch-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(agentA, "launch-1", hello("s-1")); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(agentA, "launch-1", event(EventTurnStart)); err != nil {
		t.Fatal(err)
	}
	queued, err := h.EnqueueTo(first, Deliver("for launch 1", true))
	if err != nil {
		t.Fatal(err)
	}

	second, err := h.Open(agentA, "launch-2")
	if err != nil {
		t.Fatal(err)
	}
	if second == first || second.Key != agentA {
		t.Fatalf("Open = %+v, want a new generation of %s (first %+v)", second, agentA, first)
	}
	if _, err := s.Next(testCtx(t)); !errors.Is(err, ErrForgotten) {
		t.Fatalf("predecessor's stream: err=%v, want ErrForgotten", err)
	}
	<-queued.Done()
	if !errors.Is(queued.Err(), ErrForgotten) {
		t.Fatalf("predecessor's command: err=%v, want ErrForgotten", queued.Err())
	}
	st := h.State(agentA)
	if st.Gen != second.Gen || st.Connected || st.SessionID != "" || !st.HelloAt.IsZero() || st.Busy || st.Pending != 0 {
		t.Fatalf("the new generation inherited its predecessor's state: %+v", st)
	}
}

// A mod presenting any launch but its key's current one is refused: a
// dying predecessor cannot take the successor's stream, mark it busy, or
// reject (or ack) its commands.
func TestConnectAndApplyRefuseAStaleLaunch(t *testing.T) {
	h := newTestHub(newFakeClock())
	if _, err := h.Open(agentA, "launch-old"); err != nil {
		t.Fatal(err)
	}
	current, err := h.Open(agentA, "launch-new")
	if err != nil {
		t.Fatal(err)
	}
	opening, err := h.EnqueueTo(current, Deliver("the opening", true))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.Connect(agentA, "launch-old"); !errors.Is(err, ErrStaleLaunch) {
		t.Fatalf("Connect(stale launch): err=%v, want ErrStaleLaunch", err)
	}
	for _, r := range []Report{
		hello("s-old"),
		event(EventTurnStart),
		{Type: ReportAck, ID: opening.ID, OK: false, Error: "not mine"},
	} {
		if err := h.Apply(agentA, "launch-old", r); !errors.Is(err, ErrStaleLaunch) {
			t.Fatalf("Apply(stale launch, %+v): err=%v, want ErrStaleLaunch", r, err)
		}
	}
	select {
	case <-opening.Done():
		t.Fatalf("a stale launch's ack settled the successor's command: %v", opening.Err())
	default:
	}
	if st := h.State(agentA); st.Connected || st.Busy || st.SessionID != "" || st.Pending != 1 {
		t.Fatalf("a stale launch changed the successor's state: %+v", st)
	}

	s, err := h.Connect(agentA, "launch-new")
	if err != nil {
		t.Fatalf("Connect(current launch): %v", err)
	}
	if got := mustNext(t, s); got.ID != opening.ID {
		t.Fatalf("the current launch's stream got %+v, want its opening", got)
	}
}

// A mod never creates a generation: until the daemon opens its key (a
// restarted daemon adopting the session), its stream and reports are
// refused, and nothing can be queued for a key nobody opened.
// A key no launch opened is one a restarting daemon may not have adopted
// yet: its mod is told to retry (ErrNotOpen) until the daemon has said
// which keys it is adopting. From then on only those are; any other is a
// launch that is not coming back (ErrStaleLaunch), so its mod can stop.
func TestAnUnopenedKeyIsRetryableOnlyUntilItsAdoptionIsSettled(t *testing.T) {
	h := newTestHub(newFakeClock())
	refusal := func(key string) []error {
		_, connErr := h.Connect(key, "launch-1")
		return []error{
			connErr,
			h.Apply(key, "launch-1", hello("s-1")),
			h.Apply(key, "launch-1", Report{Type: ReportAck, ID: "c1", OK: true}),
		}
	}
	expect := func(key string, want error) {
		t.Helper()
		for i, err := range refusal(key) {
			if !errors.Is(err, want) {
				t.Fatalf("%s call %d: err=%v, want %v", key, i, err, want)
			}
		}
	}
	expect("leo-awaited", ErrNotOpen)
	expect("leo-gone", ErrNotOpen)

	h.AwaitAdoption([]string{"leo-awaited", "leo-abandoned"})
	expect("leo-awaited", ErrNotOpen)
	expect("leo-gone", ErrStaleLaunch)
	h.EndAdoption("leo-abandoned")
	expect("leo-abandoned", ErrStaleLaunch)

	target := mustOpen(t, h, "leo-awaited")
	if _, err := h.Connect("leo-awaited", testLaunch); err != nil {
		t.Fatalf("the adopted launch's connect: %v", err)
	}
	h.ForgetGen(target)
	expect("leo-awaited", ErrForgotten)

	if _, err := h.Enqueue("leo-gone", Clear()); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("Enqueue: err=%v, want ErrNotOpen", err)
	}
	if st := h.State("leo-gone"); st.Gen != 0 || st.SessionID != "" {
		t.Fatalf("an unopened key grew state: %+v", st)
	}
}

func TestOpenConnectApplyValidateTheLaunch(t *testing.T) {
	h := newTestHub(newFakeClock())
	for _, launch := range []string{"", "has space", "slash/y", strings.Repeat("x", 65)} {
		if _, err := h.Open(agentA, launch); !errors.Is(err, ErrInvalidLaunch) {
			t.Fatalf("Open(%q): err=%v, want ErrInvalidLaunch", launch, err)
		}
		if _, err := h.Connect(agentA, launch); !errors.Is(err, ErrInvalidLaunch) {
			t.Fatalf("Connect(%q): err=%v, want ErrInvalidLaunch", launch, err)
		}
		if err := h.Apply(agentA, launch, hello("s")); !errors.Is(err, ErrInvalidLaunch) {
			t.Fatalf("Apply(%q): err=%v, want ErrInvalidLaunch", launch, err)
		}
	}
}

// The opening's ack says which conversation it landed in: the session the
// mod's hello named when it acked.
func TestTicketRecordsTheSessionItWasAckedIn(t *testing.T) {
	h := newTestHub(newFakeClock())
	target := mustOpen(t, h, agentA)
	tk, err := h.EnqueueTo(target, Deliver("the opening", true))
	if err != nil {
		t.Fatal(err)
	}
	if got := tk.AckedIn(); got != "" {
		t.Fatalf("AckedIn before the ack = %q", got)
	}
	_ = mustConnect(t, h, agentA)
	apply(t, h, agentA, hello("sess-landed"))
	ack(t, h, agentA, tk.ID)
	<-tk.Done()
	if got := tk.AckedIn(); got != "sess-landed" {
		t.Fatalf("AckedIn = %q, want sess-landed", got)
	}
}

func TestEventsCarryTheirGeneration(t *testing.T) {
	rec := &recorder{}
	h := newTestHub(newFakeClock(), func(o *Options) { o.Subscriber = rec })
	target := mustOpen(t, h, agentA)
	apply(t, h, agentA, hello("s-1"))
	if got := rec.snapshot(); len(got) != 1 || got[0].Gen != target.Gen {
		t.Fatalf("events = %+v, want one hello of generation %d", got, target.Gen)
	}
}

// A caller may name its command: the opening prompt's id is derived from
// its conversation and text, so re-queueing it after a relaunch or daemon
// restart reaches the mod's dedup as the same command.
func TestEnqueueToKeepsACallerID(t *testing.T) {
	h := newTestHub(newFakeClock())
	target, err := h.Open(agentA, testLaunch)
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
	target, _ := h.Open(agentA, testLaunch)
	rejected, err := h.EnqueueTo(target, Deliver("refused", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(agentA, testLaunch, Report{Type: ReportAck, ID: rejected.ID, OK: false, Error: "dropped"}); err != nil {
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
	target, _ := h.Open(agentA, testLaunch)
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
	if _, err := h.Enqueue(agentA, Clear()); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("Enqueue after the tombstone expired: err=%v, want ErrNotOpen", err)
	}
}

// A launch that is over forgets only its own generation: a successor that
// already opened the key (a stop and respawn racing the old loop) keeps
// its state.
func TestForgetGenSparesASuccessor(t *testing.T) {
	h := newTestHub(newFakeClock())
	old, _ := h.Open(agentA, "launch-old")
	successor, _ := h.Open(agentA, "launch-new")
	if _, err := h.Connect(agentA, "launch-new"); err != nil {
		t.Fatal(err)
	}

	if h.ForgetGen(old) {
		t.Fatal("ForgetGen forgot a generation that was already over")
	}
	if h.ForgetGenUnlessEverConnected(old) {
		t.Fatal("ForgetGenUnlessEverConnected forgot a generation that was already over")
	}
	if !h.Connected(agentA) {
		t.Fatal("the successor's stream was dropped")
	}
	if h.ForgetGenUnlessEverConnected(successor) {
		t.Fatal("ForgetGenUnlessEverConnected forgot a connected generation")
	}
	if !h.ForgetGen(successor) {
		t.Fatal("ForgetGen refused the current generation")
	}
	if _, err := h.Enqueue(agentA, Clear()); !errors.Is(err, ErrForgotten) {
		t.Fatalf("after ForgetGen: err=%v, want ErrForgotten", err)
	}
}

// HasConnected is the settle predicate: this generation's mod connected or
// said hello, whether or not the stream is up right now. A generation's
// state only ever holds its own launch's, so no start time is needed.
func TestStateHasConnected(t *testing.T) {
	h := newTestHub(newFakeClock())
	target := mustOpen(t, h, agentA)
	if h.State(agentA).HasConnected(target) {
		t.Fatal("connected before any connection")
	}
	s := mustConnect(t, h, agentA)
	s.Close()
	st := h.State(agentA)
	if st.Connected {
		t.Fatal("stream still connected after Close")
	}
	if !st.HasConnected(target) {
		t.Fatalf("a mod that connected must count: %+v", st)
	}
	if st.HasConnected(Target{Key: agentA, Gen: target.Gen + 1}) {
		t.Fatal("another generation's connection counted")
	}

	hello := mustOpen(t, h, agentA)
	apply(t, h, agentA, Report{Type: ReportHello, SessionID: "s", ClaudeVersion: "2.1.289"})
	if !h.State(agentA).HasConnected(hello) {
		t.Fatal("a hello without a stream must count")
	}
}
