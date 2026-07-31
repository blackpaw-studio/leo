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
	ch, unsub := b.Subscribe(4)
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
	chA, unsubA := b.Subscribe(4)
	chB, unsubB := b.Subscribe(4)
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
	ch, unsub := b.Subscribe(4)

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
	_, unsub := b.Subscribe(1)

	// Act + Assert: must not panic on double-close.
	unsub()
	unsub()
}

func TestBusSlowSubscriberIsDroppedNotBlocked(t *testing.T) {
	// Arrange
	b := NewBus()
	ch, _ := b.Subscribe(1) // unbuffered beyond 1 slot

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
	ch, unsub := b.Subscribe(4)
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
			ch, unsub := b.Subscribe(2)
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
