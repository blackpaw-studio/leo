package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// signalCh is closed-over by runtime hooks so tests block on the event they
// need instead of sleeping.
func signalCh() (fire func(), ch chan struct{}) {
	ch = make(chan struct{}, 16)
	return func() { ch <- struct{}{} }, ch
}

func expectSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func expectNoSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected %s", what)
	default:
	}
}

// idleInteractive starts an interactive claude run and finishes its opening
// turn, leaving it idle and holding no slot.
func idleInteractive(t *testing.T, d *Dispatcher) string {
	t.Helper()
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if started.Queued {
		t.Fatal("setup run queued")
	}
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	if rec, _ := d.Get(started.ID); rec.Status != StatusIdle {
		t.Fatalf("setup run status = %s, want idle", rec.Status)
	}
	return started.ID
}

func lastTurn(t *testing.T, d *Dispatcher, id string) Turn {
	t.Helper()
	rec, _ := d.Get(id)
	if len(rec.Turns) == 0 {
		t.Fatalf("%s has no turns", id)
	}
	return rec.Turns[len(rec.Turns)-1]
}

func TestSendQueuesInsteadOfRejectingWhenEverySlotIsBusy(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	injected, injectedCh := signalCh()
	rt.injectHook = injected
	freeOne := fillSlots(d)

	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatalf("send with every slot busy = %v, want it queued", err)
	}
	if !sent.Queued || sent.Delivered || sent.TurnID != id+"#2" {
		t.Fatalf("send result = %+v, want queued turn %s#2", sent, id)
	}
	turn := lastTurn(t, d, id)
	if turn.Outcome != "" || !turn.Queued || turn.SlotHeld {
		t.Fatalf("queued turn = %+v, want open, queued, holding no slot", turn)
	}
	if rec, _ := d.Get(id); rec.Status != StatusQueued {
		t.Fatalf("run status = %s, want queued", rec.Status)
	}
	expectNoSignal(t, injectedCh, "injection before a slot freed")

	freeOne()
	expectSignal(t, injectedCh, "queued follow-up injection")
	turn = lastTurn(t, d, id)
	if turn.Queued || !turn.SlotHeld {
		t.Fatalf("started turn = %+v, want it to hold the freed slot", turn)
	}
	if got := d.slots.InUse(); got != d.slots.Max() {
		t.Fatalf("slots in use = %d, want the freed slot taken", got)
	}
}

func TestQueuedFollowUpEmitsNoRejection(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	fillSlots(d)

	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	for _, turn := range rec.Turns {
		if turn.Outcome == TurnRejected {
			t.Fatalf("turn %s rejected: %+v", turn.TurnID, turn)
		}
	}
	if n, ok := rec.Notifications[sent.TurnID]; ok {
		t.Fatalf("queued turn already has a completion notification: %+v", n)
	}
}

func TestQueuedFollowUpsAndNewRunsShareOneFIFO(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	idle := idleInteractive(t, d)
	launched, launchedCh := signalCh()
	injected, injectedCh := signalCh()
	rt.launchHook, rt.injectHook = launched, injected
	freeOne := fillSlots(d)

	newRun, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "y", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil || !newRun.Queued {
		t.Fatalf("new run = %+v, %v; want queued", newRun, err)
	}
	if _, err := d.Send(context.Background(), idle, "follow-up"); err != nil {
		t.Fatal(err)
	}

	freeOne()
	expectSignal(t, launchedCh, "queued new run launch (first in line)")
	expectNoSignal(t, injectedCh, "follow-up injection ahead of its turn")

	freeOne()
	expectSignal(t, injectedCh, "queued follow-up injection (second in line)")
}

func TestCancelWhileFollowUpQueuedResolvesTheTurn(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	injected, injectedCh := signalCh()
	rt.injectHook = injected
	freeOne := fillSlots(d)
	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]
	if entry.Outcome != TurnInterrupted {
		t.Fatalf("wait on canceled queued turn = %+v, want outcome interrupted", entry)
	}
	turn := lastTurn(t, d, id)
	if turn.Queued || turn.SlotHeld {
		t.Fatalf("resolved turn = %+v, want no queued/slot flags", turn)
	}
	if d.slots.Waiting() != 0 {
		t.Fatalf("%d waiters left in line after cancel", d.slots.Waiting())
	}
	before := d.slots.InUse()
	freeOne()
	if d.slots.InUse() != before-1 {
		t.Fatalf("slots in use = %d, want the freed slot left free (was %d)", d.slots.InUse(), before)
	}
	expectNoSignal(t, injectedCh, "injection for a canceled turn")
}

func TestSteeringWhileFollowUpQueuedResolvesTheTurn(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	injected, injectedCh := signalCh()
	rt.injectHook = injected
	freeOne := fillSlots(d)
	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatal(err)
	}

	_ = d.Report(id, hookWithPrompt(t, "UserPromptSubmit", "u1", "typed by a human"))
	rec, _ := d.Get(id)
	var queued Turn
	for _, turn := range rec.Turns {
		if turn.TurnID == sent.TurnID {
			queued = turn
		}
	}
	if queued.Outcome != TurnLost || queued.Queued || queued.Text == "" {
		t.Fatalf("steered queued turn = %+v, want lost with an explanation", queued)
	}
	if d.slots.Waiting() != 0 {
		t.Fatalf("%d waiters left in line after steering", d.slots.Waiting())
	}
	freeOne()
	expectNoSignal(t, injectedCh, "injection for a steered-away turn")
}

func TestSecondFollowUpWhileOneIsQueuedIsRejectedClearly(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	fillSlots(d)
	sent, err := d.Send(context.Background(), id, "one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Send(context.Background(), id, "two")
	if err == nil || !strings.Contains(err.Error(), sent.TurnID) {
		t.Fatalf("second send err = %v, want it to point at queued turn %s", err, sent.TurnID)
	}
}

func TestPermissionDecisionNeverNeedsASlot(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	id := idleInteractive(t, d)
	fillSlots(d)
	before := d.slots.InUse()
	// An unknown request is an error, but it must be the decision error, not
	// a capacity one, and it must not touch the limiter.
	_, err := d.Decide(id, Decision{Behavior: "allow", RequestID: "nope"})
	if err != nil && strings.Contains(err.Error(), "capacity") {
		t.Fatalf("decision hit capacity: %v", err)
	}
	if d.slots.InUse() != before || d.slots.Waiting() != 0 {
		t.Fatalf("decision changed the limiter: in use %d -> %d, waiting %d", before, d.slots.InUse(), d.slots.Waiting())
	}
}

func TestStartHonorsConfiguredMaxConcurrent(t *testing.T) {
	one := 1
	cfg := testConfig()
	cfg.Defaults.Dispatch.MaxConcurrent = &one
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	req := Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive}

	first, err := d.Start(context.Background(), cfg, req)
	if err != nil || first.Queued {
		t.Fatalf("first = %+v, %v; want it to start", first, err)
	}
	second, err := d.Start(context.Background(), cfg, req)
	if err != nil || !second.Queued {
		t.Fatalf("second = %+v, %v; want it queued behind max_concurrent: 1", second, err)
	}
}

func TestZeroMaxConcurrentIsUnlimited(t *testing.T) {
	zero := 0
	cfg := testConfig()
	cfg.Defaults.Dispatch.MaxConcurrent = &zero
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	req := Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive}

	for i := 0; i <= DefaultMaxConcurrent+1; i++ {
		started, err := d.Start(context.Background(), cfg, req)
		if err != nil || started.Queued {
			t.Fatalf("start %d = %+v, %v; want unlimited", i, started, err)
		}
	}
}

func TestSlotLimiterGrantsInEnqueueOrderAndCancelLeavesTheLine(t *testing.T) {
	l := newSlotLimiter(1)
	if !l.TryAcquire() {
		t.Fatal("empty limiter refused a slot")
	}
	first, second, third := l.Enqueue(), l.Enqueue(), l.Enqueue()
	l.Cancel(second)

	l.Release()
	select {
	case <-first.Ready():
	default:
		t.Fatal("first in line not granted")
	}
	select {
	case <-third.Ready():
		t.Fatal("third granted ahead of first")
	default:
	}
	l.Release()
	select {
	case <-third.Ready():
	default:
		t.Fatal("canceled waiter was not skipped")
	}
	if l.TryAcquire() {
		t.Fatal("TryAcquire barged past a full limiter")
	}
}

func TestSlotLimiterTryAcquireDoesNotBargePastWaiters(t *testing.T) {
	l := newSlotLimiter(1)
	l.TryAcquire()
	w := l.Enqueue()
	l.SetMax(2)
	select {
	case <-w.Ready():
	default:
		t.Fatal("raising the cap did not admit the waiter")
	}
	if l.TryAcquire() {
		t.Fatal("limiter over its cap")
	}
}
