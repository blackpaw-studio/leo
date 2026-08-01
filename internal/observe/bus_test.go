package observe

import (
	"sync"
	"testing"
	"time"
)

func withFixedBusClock(t *testing.T, at time.Time) {
	t.Helper()
	orig := busNow
	busNow = func() time.Time { return at }
	t.Cleanup(func() { busNow = orig })
}

func TestBusPublishStampsSeqAndTime(t *testing.T) {
	// Arrange
	fixed := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	withFixedBusClock(t, fixed)
	b := NewBus()
	ch, unsub, _ := b.Subscribe(4)
	defer unsub()

	// Act
	b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "den"}})
	b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "den"}})

	// Assert
	first := <-ch
	second := <-ch
	p1 := first.Payload.(*AgentStoppedPayload)
	p2 := second.Payload.(*AgentStoppedPayload)
	if p1.Seq != 1 || p2.Seq != 2 {
		t.Fatalf("expected sequential seq 1,2, got %d,%d", p1.Seq, p2.Seq)
	}
	if !p1.At.Equal(fixed) || !p2.At.Equal(fixed) {
		t.Fatalf("expected stamped time %v, got %v and %v", fixed, p1.At, p2.At)
	}
}

func TestBusFansOutToAllSubscribers(t *testing.T) {
	// Arrange
	b := NewBus()
	chA, unsubA, _ := b.Subscribe(4)
	chB, unsubB, _ := b.Subscribe(4)
	defer unsubA()
	defer unsubB()

	// Act
	b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "den"}})

	// Assert
	evA := <-chA
	evB := <-chB
	if evA.Payload.(*AgentStoppedPayload).Agent != "den" || evB.Payload.(*AgentStoppedPayload).Agent != "den" {
		t.Fatalf("expected both subscribers to receive the event")
	}
}

func TestBusUnsubscribeStopsDelivery(t *testing.T) {
	// Arrange
	b := NewBus()
	ch, unsub, _ := b.Subscribe(4)

	// Act
	unsub()
	b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "den"}})

	// Assert: channel is closed and drained, never delivers the event.
	_, open := <-ch
	if open {
		t.Fatalf("expected channel closed after unsubscribe")
	}
}

func TestBusUnsubscribeIsSafeToCallTwice(t *testing.T) {
	// Arrange
	b := NewBus()
	_, unsub, _ := b.Subscribe(1)

	// Act + Assert: must not panic on double-close.
	unsub()
	unsub()
}

func TestBusSlowSubscriberIsDroppedNotBlocked(t *testing.T) {
	// Arrange
	b := NewBus()
	ch, _, _ := b.Subscribe(1) // unbuffered beyond 1 slot

	// Act: fill the buffer, then publish again without draining. The
	// publisher must return promptly rather than blocking.
	done := make(chan struct{})
	go func() {
		b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "a"}})
		b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "b"}})
		b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "c"}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// Assert: subscriber's channel was closed (dropped) rather than growing
	// unbounded or blocking the publisher.
	<-ch // drain the one buffered event
	_, open := <-ch
	if open {
		t.Fatalf("expected slow subscriber's channel to be closed")
	}
}

// TestBusPublishDeliveryIsMonotonicUnderConcurrentPublishers forces the exact
// race window Publish must close: one publisher is stalled right after its
// seq is assigned (but before it stamps/delivers), while a second publisher
// races to completion behind it. Without a dedicated send lock held across
// that whole window, the second publisher's higher seq is delivered first —
// a subscriber would see seq go backwards.
func TestBusPublishDeliveryIsMonotonicUnderConcurrentPublishers(t *testing.T) {
	// Arrange
	b := NewBus()
	ch, unsub, _ := b.Subscribe(4)
	defer unsub()

	started := make(chan struct{})
	release := make(chan struct{})
	origHook := publishTestHook
	t.Cleanup(func() { publishTestHook = origHook })
	publishTestHook = func(seq uint64) {
		if seq == 1 {
			close(started)
			<-release
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "first"}})
	}()
	go func() {
		defer wg.Done()
		<-started
		second := make(chan struct{})
		go func() {
			b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "second"}})
			close(second)
		}()
		// Give the second publisher a real chance to race ahead of the
		// stalled first one before letting the first proceed.
		time.Sleep(20 * time.Millisecond)
		close(release)
		<-second
	}()
	wg.Wait()

	// Act
	firstDelivered := <-ch
	secondDelivered := <-ch

	// Assert
	fp := firstDelivered.Payload.(*AgentStoppedPayload)
	sp := secondDelivered.Payload.(*AgentStoppedPayload)
	if fp.Seq != 1 || sp.Seq != 2 {
		t.Fatalf("expected delivery order seq 1 then 2, got %d then %d", fp.Seq, sp.Seq)
	}
}

func TestBusConcurrentPublishAndSubscribeIsRaceFree(t *testing.T) {
	// Arrange
	b := NewBus()
	var wg sync.WaitGroup

	// Act: hammer Publish, Subscribe, and unsubscribe concurrently.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, unsub, _ := b.Subscribe(2)
			defer unsub()
			select {
			case <-ch:
			case <-time.After(50 * time.Millisecond):
			}
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "x"}})
		}()
	}
	wg.Wait()
}

// TestHelloSeqSubscribeRaceWindow deterministically exercises a Subscribe
// call that lands while a Publish is in flight but has not yet delivered:
// using publishTestHook, it pauses a Publish right after it has bumped seq
// and snapshotted subscribers (which does not yet include the one about to
// subscribe) but before it stamps or delivers its event, subscribes during
// that pause, then lets the paused publish finish. It proves that event is
// correctly excluded from delivery even though its seq was already counted,
// and that the very next published event is exactly helloSeq+1 — on every
// run, not just probabilistically.
//
// Note this does not reproduce the literal historical bug Subscribe's own
// doc comment warns about (reading the starting seq via a second, separate
// lock acquisition after registration): publishTestHook only fires inside
// Publish, after Publish's own critical section has already released the
// bus's lock, so it cannot force a fresh Publish to start and fully
// complete strictly between a Subscribe's register step and its (would-be)
// separate read step — there is no test seam inside Subscribe itself to
// pause at that point, and the current implementation has no such window to
// pause in (registration and the seq read happen under one lock
// acquisition). Reinstating the old two-lock composition and rerunning the
// prior 500-iteration unsynchronized version of this test confirms that
// composition is still broken (3/3 failures under -race); what closes that
// window is Subscribe's atomicity itself, per its doc comment, not this
// test.
func TestHelloSeqSubscribeRaceWindow(t *testing.T) {
	b := NewBus()

	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	origHook := publishTestHook
	publishTestHook = func(uint64) {
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	t.Cleanup(func() { publishTestHook = origHook })

	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "x"}})
	}()

	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight publish to reach the seq-assignment hook")
	}

	// The in-flight publish above has already bumped b.seq and snapshotted
	// its subscriber list (which does not include us yet) but has not
	// stamped or delivered its event. Subscribing here is exactly the
	// window under test.
	ch, unsub, helloSeq := b.Subscribe(4)
	t.Cleanup(unsub)

	close(release)
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the paused publish to complete")
	}

	// The in-flight publish's event predates our subscription and must not
	// be delivered to us, even though its seq was already counted.
	select {
	case ev := <-ch:
		t.Fatalf("unexpectedly received the in-flight publish's event: %+v", ev)
	default:
	}

	b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "x"}})

	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(*AgentStoppedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *AgentStoppedPayload", ev.Payload)
		}
		if payload.Seq != helloSeq+1 {
			t.Fatalf("first delivered seq = %d, want helloSeq+1 = %d", payload.Seq, helloSeq+1)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the post-subscribe event")
	}
}

// TestHelloSeqUnderConcurrentPublishers is the regression guard for the
// historical two-lock composition: registering a subscriber and then reading
// the starting sequence through a separate call, which let a concurrent
// Publish bump that sequence past what the subscriber would actually receive.
//
// Unlike TestHelloSeqSubscribeRaceWindow, which proves the invariant
// deterministically but cannot express that interleaving, this one hammers
// the window with real concurrency. It is probabilistic, but reliably caught
// the defect under -race, which is how this package's tests always run.
func TestHelloSeqUnderConcurrentPublishers(t *testing.T) {
	b := NewBus()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				b.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "x"}})
			}
		}
	}()
	t.Cleanup(func() { close(stop) })

	for i := 0; i < 500; i++ {
		ch, unsub, helloSeq := b.Subscribe(4)
		select {
		case ev := <-ch:
			payload, ok := ev.Payload.(*AgentStoppedPayload)
			if !ok {
				unsub()
				t.Fatalf("iteration %d: unexpected payload type %T", i, ev.Payload)
			}
			if payload.Seq != helloSeq+1 {
				unsub()
				t.Fatalf("iteration %d: first delivered seq = %d, want helloSeq+1 = %d", i, payload.Seq, helloSeq+1)
			}
		case <-time.After(time.Second):
			unsub()
			t.Fatalf("iteration %d: timed out waiting for an event", i)
		}
		unsub()
	}
}
