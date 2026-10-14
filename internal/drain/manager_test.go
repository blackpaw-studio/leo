package drain

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type harness struct {
	m        *Manager
	clock    *fakeClock
	prober   *fakeProber
	notifier *fakeNotifier
	cron     *fakeCron
	store    *fakeStore
}

func newHarness(t *testing.T, probes ...Probe) *harness {
	t.Helper()
	h := &harness{
		clock:    newFakeClock(),
		prober:   newFakeProber(probes...),
		notifier: newFakeNotifier(),
		cron:     &fakeCron{},
		store:    &fakeStore{},
	}
	h.m = New(Deps{
		Prober:   h.prober,
		Notifier: h.notifier,
		Cron:     h.cron,
		Store:    h.store,
		Clock:    h.clock,
		Logf:     t.Logf,
		// Notices go out synchronously so each test reads their effect
		// right after the call that sent them.
		Go: func(f func()) { f() },
	})
	return h
}

func bridgeBusy(name string) Probe {
	return Probe{Name: name, Exists: true, Busy: true, Transport: TransportBridge}
}

func composerBusy(name string) Probe {
	return Probe{Name: name, Exists: true, Busy: true, Transport: TransportComposer}
}

func (h *harness) begin(t *testing.T) Status {
	t.Helper()
	st, err := h.m.Begin(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return st
}

// tick advances one poll interval, renews the lease the way a polling CLI
// does, and ticks.
func (h *harness) tick() Status {
	h.clock.Advance(2 * time.Second)
	h.m.Status()
	h.m.Tick(context.Background())
	return h.m.Status()
}

func memberState(st Status, name string) MemberState {
	for _, m := range st.Members {
		if m.Name == name {
			return m.State
		}
	}
	return ""
}

func TestBeginTakesBusySetAndHoldsCron(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"), composerBusy("b"), Probe{Name: "idle", Exists: true, Transport: TransportComposer})
	st := h.begin(t)
	if st.ID == "" || st.Phase != PhaseDraining || st.Done {
		t.Fatalf("Begin status = %+v, want a draining, not-done drain with an ID", st)
	}
	if st.TimeoutMinutes != 10 {
		t.Errorf("TimeoutMinutes = %d, want 10", st.TimeoutMinutes)
	}
	var names []string
	for _, m := range st.Members {
		names = append(names, m.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("members = %v, want [a b]", names)
	}
	if !h.cron.held {
		t.Fatal("cron not held")
	}
	if _, err := h.m.Begin(context.Background(), time.Minute); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second Begin err = %v, want ErrInProgress", err)
	}
}

func TestBeginWithNoBusyAgentsIsDone(t *testing.T) {
	h := newHarness(t, Probe{Name: "idle", Exists: true, Transport: TransportComposer})
	st := h.begin(t)
	if !st.Done || len(st.Members) != 0 {
		t.Fatalf("Begin status = %+v, want done with no members", st)
	}
	if err := h.m.Commit(context.Background(), false); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestBridgeMemberGetsPauseOnceFromBegin(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"))
	st := h.begin(t)
	want := []sentMessage{{"a", PauseText(10 * time.Minute), "drain-" + st.ID + "-a"}}
	if got := h.notifier.messages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages after Begin = %+v, want %+v", got, want)
	}
	h.prober.set("a", false)
	h.tick()
	h.tick()
	if got := h.notifier.messages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages after ticks = %+v, want only the one pause", got)
	}
}

func TestBridgeQueuedMemberNotifiedOnlyAfterOutboxReleasesIt(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"))
	h.notifier.queued["a"] = true
	st := h.begin(t)
	id := "drain-" + st.ID + "-a"
	if got := memberState(st, "a"); got != StateWaiting {
		t.Fatalf("state with pause queued = %q, want waiting", got)
	}
	h.prober.set("a", false)
	for i := 0; i < 4; i++ {
		if st := h.tick(); st.Done || memberState(st, "a") != StateWaiting {
			t.Fatalf("tick %d with pause still queued: %+v, want waiting and not done", i, st)
		}
	}
	h.notifier.ack(id)
	if st := h.tick(); memberState(st, "a") != StateNotified || st.Done {
		t.Fatalf("after ack: %+v, want notified and not done", st)
	}
	h.tick()
	if st := h.tick(); !st.Done || memberState(st, "a") != StateDrained {
		t.Fatalf("after settle polls: %+v, want drained and done", st)
	}
}

func TestComposerMemberMessagedOnFirstIdleTickAndRetried(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	st := h.begin(t)
	if len(h.notifier.messages()) != 0 {
		t.Fatal("composer member messaged while busy at Begin")
	}
	h.tick()
	if len(h.notifier.messages()) != 0 {
		t.Fatal("composer member messaged while still busy")
	}
	h.prober.set("b", false)
	h.notifier.setFail("b", errors.New("composer busy"))
	if st := h.tick(); memberState(st, "b") != StateWaiting {
		t.Fatalf("after failed send: %+v, want waiting", st)
	}
	h.notifier.setFail("b", nil)
	h.tick()
	want := []sentMessage{{"b", PauseText(10 * time.Minute), "drain-" + st.ID + "-b"}}
	if got := h.notifier.messages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %+v, want %+v", got, want)
	}
}

func TestMemberDrainsAfterSettlePollsAndWaitsAgainWhenBusy(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	h.begin(t)
	h.prober.set("b", false)
	if st := h.tick(); memberState(st, "b") != StateNotified {
		t.Fatalf("after pause pasted: %+v, want notified", st)
	}
	h.prober.set("b", true) // the pause turn runs
	h.tick()
	h.prober.set("b", false)
	if st := h.tick(); st.Done {
		t.Fatalf("done after one idle poll: %+v", st)
	}
	if st := h.tick(); !st.Done || memberState(st, "b") != StateDrained {
		t.Fatalf("after two idle polls: %+v, want drained", st)
	}
	h.prober.set("b", true)
	if st := h.tick(); st.Done || memberState(st, "b") != StateNotified {
		t.Fatalf("re-busy: %+v, want notified and not done", st)
	}
	h.prober.set("b", false)
	h.tick()
	if st := h.tick(); !st.Done {
		t.Fatalf("idle again: %+v, want done", st)
	}
}

func TestBusyThenIdleWithoutNotificationIsNotDone(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	h.begin(t)
	h.notifier.setFail("b", errors.New("paste failed"))
	h.prober.set("b", false)
	for i := 0; i < 5; i++ {
		if st := h.tick(); st.Done {
			t.Fatalf("tick %d: done without the pause ever landing", i)
		}
	}
}

func TestGoneMemberCountsAsDrained(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	h.begin(t)
	h.prober.remove("b")
	if st := h.tick(); !st.Done || memberState(st, "b") != StateGone {
		t.Fatalf("after removal: %+v, want gone and done", st)
	}
}

func TestNotifierUnavailableFallsBackToWaiting(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"), composerBusy("b"))
	h.notifier.setFail("a", ErrNotifierUnavailable)
	h.notifier.setFail("b", ErrNotifierUnavailable)
	st := h.begin(t)
	if st.Warning == "" {
		t.Fatal("no warning without a notifier")
	}
	h.prober.set("a", false)
	h.prober.set("b", false)
	h.tick()
	if st := h.tick(); !st.Done {
		t.Fatalf("after settle polls: %+v, want done", st)
	}
}

func TestAgentsOutsideTheDrainSetAreNeverTouched(t *testing.T) {
	h := newHarness(t, composerBusy("b"), Probe{Name: "other", Exists: true, Transport: TransportComposer})
	h.begin(t)
	h.prober.set("other", true)
	h.prober.set("b", false)
	h.tick()
	h.tick()
	for _, name := range h.prober.probedNames() {
		if name != "b" {
			t.Fatalf("probed %q outside the drain set", name)
		}
	}
	for _, msg := range h.notifier.messages() {
		if msg.name != "b" {
			t.Fatalf("messaged %q outside the drain set", msg.name)
		}
	}
}

func TestCommitRequiresDoneUnlessForced(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"), composerBusy("b"))
	h.cron.tasks = []string{"heartbeat"}
	st := h.begin(t)
	if err := h.m.Commit(context.Background(), false); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("Commit(false) err = %v, want ErrNotDrained", err)
	}
	if len(h.store.writes) != 0 {
		t.Fatal("store written by a refused commit")
	}
	if err := h.m.Commit(context.Background(), true); err != nil {
		t.Fatalf("Commit(true): %v", err)
	}
	want := PendingResume{DrainID: st.ID, Agents: []string{"a", "b"}, DeferredTasks: []string{"heartbeat"}, WrittenAt: h.clock.Now()}
	if !reflect.DeepEqual(h.store.writes, []PendingResume{want}) {
		t.Fatalf("store writes = %+v, want %+v", h.store.writes, want)
	}
	if got := h.m.Status(); got.Phase != PhaseCommitted {
		t.Fatalf("phase = %q, want committed", got.Phase)
	}
	if !h.cron.held || h.cron.released != 0 {
		t.Fatal("commit released the cron hold")
	}
}

func TestCommitRunsAFreshTick(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"))
	h.begin(t)
	h.prober.set("a", false)
	h.tick()
	h.tick() // second idle poll after the pause was delivered by Begin
	h.m.Status()
	if err := h.m.Commit(context.Background(), false); err != nil {
		t.Fatalf("Commit after drained: %v", err)
	}
	h2 := newHarness(t, bridgeBusy("a"))
	h2.begin(t)
	h2.prober.set("a", false)
	h2.tick()
	// The commit's own tick is the second idle poll.
	if err := h2.m.Commit(context.Background(), false); err != nil {
		t.Fatalf("Commit whose tick completes the drain: %v", err)
	}
}

func TestCancelMessagesOnlyMembersThatGotThePause(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"), bridgeBusy("q"), composerBusy("b"), composerBusy("c"))
	h.notifier.queued["q"] = true
	st := h.begin(t)
	h.prober.set("c", false)
	h.tick() // c gets its pause pasted; b is still busy and never does
	before := len(h.notifier.messages())
	if err := h.m.Cancel(context.Background(), "aborted"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var cancelled []string
	for _, msg := range h.notifier.messages()[before:] {
		if msg.text != CancelText {
			t.Fatalf("cancel sent %q", msg.text)
		}
		cancelled = append(cancelled, msg.name)
	}
	if !reflect.DeepEqual(cancelled, []string{"a", "c", "q"}) {
		t.Fatalf("cancel recipients = %v, want [a c q]", cancelled)
	}
	if h.cron.released != 1 || h.store.removes != 1 {
		t.Fatalf("released %d, removes %d; want 1, 1", h.cron.released, h.store.removes)
	}
	if got := h.m.Status(); got.Phase != PhaseIdle || got.ID != "" {
		t.Fatalf("status after cancel = %+v, want idle", got)
	}
	if _, err := h.m.Begin(context.Background(), time.Minute); err != nil {
		t.Fatalf("Begin after cancel: %v", err)
	}
	_ = st
}

func TestCancelAfterCommit(t *testing.T) {
	h := newHarness(t, bridgeBusy("a"))
	h.begin(t)
	if err := h.m.Commit(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Cancel(context.Background(), "restart failed"); err != nil {
		t.Fatalf("Cancel after commit: %v", err)
	}
	if h.cron.released != 1 || h.store.removes != 1 {
		t.Fatalf("released %d, removes %d; want 1, 1", h.cron.released, h.store.removes)
	}
}

func TestCancelWithoutDrain(t *testing.T) {
	h := newHarness(t)
	if err := h.m.Cancel(context.Background(), "x"); !errors.Is(err, ErrNoDrain) {
		t.Fatalf("Cancel err = %v, want ErrNoDrain", err)
	}
	if err := h.m.Commit(context.Background(), true); !errors.Is(err, ErrNoDrain) {
		t.Fatalf("Commit err = %v, want ErrNoDrain", err)
	}
}

func TestLeaseExpiresAfterTTLWithoutStatus(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	h.begin(t)
	h.clock.Advance(60 * time.Second)
	h.m.Tick(context.Background())
	if got := h.m.Status(); got.Phase != PhaseDraining {
		t.Fatalf("phase at TTL = %q, want draining", got.Phase)
	}
	h.clock.Advance(61 * time.Second)
	h.m.Tick(context.Background())
	if h.cron.released != 1 {
		t.Fatal("lease did not cancel the drain at TTL+1s")
	}
	if got := h.m.Status(); got.Phase != PhaseIdle {
		t.Fatalf("phase after lease = %q, want idle", got.Phase)
	}
}

func TestLeaseExpiresWhileCommitted(t *testing.T) {
	h := newHarness(t, composerBusy("b"))
	h.begin(t)
	if err := h.m.Commit(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(61 * time.Second)
	h.m.Tick(context.Background())
	if h.cron.released != 1 || h.store.removes != 1 {
		t.Fatalf("released %d, removes %d; want the committed drain cancelled", h.cron.released, h.store.removes)
	}
}

// slowNotifier holds every send until release closes, reporting each one
// as it starts.
type slowNotifier struct {
	started chan string
	release chan struct{}
}

func (n slowNotifier) Message(_ context.Context, name, _, _ string) (Delivery, error) {
	n.started <- name
	<-n.release
	return Delivery{Transport: TransportBridge}, nil
}

func (slowNotifier) Holds(string, string) (bool, error) { return false, nil }

// Each bridge delivery can block for seconds; Begin answers an IPC call
// with its own timeout, so it must not wait on them, and the notices go
// out side by side rather than one after another.
func TestBeginDoesNotWaitOnPauseNotices(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	var probes []Probe
	for _, n := range names {
		probes = append(probes, bridgeBusy(n))
	}
	slow := slowNotifier{started: make(chan string, len(names)), release: make(chan struct{})}
	var inFlight sync.WaitGroup
	m := New(Deps{
		Prober:   newFakeProber(probes...),
		Notifier: slow,
		Cron:     &fakeCron{},
		Store:    &fakeStore{},
		Clock:    newFakeClock(),
		Logf:     t.Logf,
		Go: func(f func()) {
			inFlight.Add(1)
			go func() {
				defer inFlight.Done()
				f()
			}()
		},
	})
	t.Cleanup(func() {
		select {
		case <-slow.release:
		default:
			close(slow.release)
		}
		inFlight.Wait()
	})

	st := beginWithin(t, m)
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	for _, mem := range st.Members {
		if mem.State != StateWaiting {
			t.Fatalf("%s is %s before its notice landed, want waiting", mem.Name, mem.State)
		}
	}
	for range names { // all five are in flight at once
		select {
		case <-slow.started:
		case <-guard.C:
			t.Fatal("pause notices were not sent concurrently")
		}
	}
	close(slow.release)
	inFlight.Wait()
	for _, mem := range m.Status().Members {
		if mem.State != StateNotified {
			t.Fatalf("%s is %s after its notice landed, want notified", mem.Name, mem.State)
		}
	}
}

// A drain cancelled while a pause notice is in flight still owes that
// agent a cancel once the notice lands.
func TestCancelWhilePauseInFlightSendsCancelAfterIt(t *testing.T) {
	slow := slowNotifier{started: make(chan string, 2), release: make(chan struct{})}
	rec := newFakeNotifier()
	var inFlight sync.WaitGroup
	m := New(Deps{
		Prober:   newFakeProber(bridgeBusy("a")),
		Notifier: &switchNotifier{first: slow, then: rec},
		Cron:     &fakeCron{},
		Store:    &fakeStore{},
		Clock:    newFakeClock(),
		Go: func(f func()) {
			inFlight.Add(1)
			go func() {
				defer inFlight.Done()
				f()
			}()
		},
	})
	t.Cleanup(func() {
		select {
		case <-slow.release:
		default:
			close(slow.release)
		}
	})
	beginWithin(t, m)
	<-slow.started
	if err := m.Cancel(context.Background(), "aborted"); err != nil {
		t.Fatal(err)
	}
	close(slow.release)
	inFlight.Wait()
	got := rec.messages()
	if len(got) != 1 || got[0].name != "a" || got[0].text != CancelText {
		t.Fatalf("after the in-flight pause landed: %+v, want one cancel to a", got)
	}
}

// beginWithin starts a drain and fails the test if Begin blocks on the
// notices it sends (the guard only fires on that failure).
func beginWithin(t *testing.T, m *Manager) Status {
	t.Helper()
	begun := make(chan Status, 1)
	go func() {
		st, err := m.Begin(context.Background(), 10*time.Minute)
		if err != nil {
			t.Errorf("Begin: %v", err)
		}
		begun <- st
	}()
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	select {
	case st := <-begun:
		return st
	case <-guard.C:
		t.Fatal("Begin waited on the pause notices")
		return Status{}
	}
}

// switchNotifier sends the first message through first and the rest
// through then.
type switchNotifier struct {
	mu    sync.Mutex
	used  bool
	first Notifier
	then  Notifier
}

func (s *switchNotifier) Message(ctx context.Context, name, text, id string) (Delivery, error) {
	s.mu.Lock()
	n := s.then
	if !s.used {
		s.used, n = true, s.first
	}
	s.mu.Unlock()
	return n.Message(ctx, name, text, id)
}

func (s *switchNotifier) Holds(name, id string) (bool, error) { return s.then.Holds(name, id) }
