package observe

import (
	"testing"
	"time"
)

func TestProgressCoalesceFirstWriteIsImmediate(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	if n := len(h.taskEvents("a")); n != 1 {
		t.Fatalf("published %d events", n)
	}
}

func TestProgressCoalesceBurstCollapsesToLeadingAndTrailing(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T"), Percent: ptr(1.0)})
	for i := 2; i <= 6; i++ {
		h.clock.Advance(50 * time.Millisecond)
		h.up("a", TaskUpdate{ID: "t", Percent: ptr(float64(i))})
	}
	if n := len(h.taskEvents("a")); n != 1 {
		t.Fatalf("burst must hold back until the window ends, published %d", n)
	}

	h.clock.Advance(TaskPublishInterval)

	events := h.taskEvents("a")
	if len(events) != 2 {
		t.Fatalf("want leading + trailing, got %d", len(events))
	}
	last := events[1].Task
	if *last.Percent != 6 || last.Revision != 6 {
		t.Fatalf("trailing event must carry the latest state: %+v", last)
	}
	h.clock.Advance(10 * time.Second)
	if len(h.taskEvents("a")) != 2 {
		t.Fatal("no further publishes without writes")
	}
}

// timedPublisher records the clock reading of every publish.
type timedPublisher struct {
	clock *fakeClock
	at    []time.Time
}

func (p *timedPublisher) Publish(Event) { p.at = append(p.at, p.clock.Now()) }

func TestProgressCoalesceAtMostTwoPerSecond(t *testing.T) {
	clock := newFakeClock(progressT0)
	pub := &timedPublisher{clock: clock}
	store := NewProgressStore(clock, pub)
	for i := 0; i < 100; i++ { // a write every 30ms for 3 seconds
		if _, err := store.Upsert("a", TaskUpdate{ID: "t", Title: ptr("T"), Percent: ptr(float64(i))}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(30 * time.Millisecond)
	}
	clock.Advance(TaskPublishInterval)

	if len(pub.at) < 6 {
		t.Fatalf("published only %d events over ~3s", len(pub.at))
	}
	for i := 2; i < len(pub.at); i++ {
		if span := pub.at[i].Sub(pub.at[i-2]); span < time.Second {
			t.Fatalf("publishes %d..%d span %v: more than two per second", i-2, i, span)
		}
	}
}

func TestProgressCoalesceWriteAfterQuietPeriodIsImmediate(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	h.clock.Advance(TaskPublishInterval)

	h.up("a", TaskUpdate{ID: "t", Percent: ptr(5.0)})

	if n := len(h.taskEvents("a")); n != 2 {
		t.Fatalf("published %d", n)
	}
}

func TestProgressCoalesceTerminalStateFlushesImmediately(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	h.clock.Advance(10 * time.Millisecond)
	h.up("a", TaskUpdate{ID: "t", Percent: ptr(50.0)}) // held back
	h.clock.Advance(10 * time.Millisecond)

	h.up("a", TaskUpdate{ID: "t", State: TaskFailed})

	events := h.taskEvents("a")
	if len(events) != 2 || events[1].Task.State != TaskFailed {
		t.Fatalf("terminal state must publish at once: %d events", len(events))
	}
	h.clock.Advance(time.Second)
	if len(h.taskEvents("a")) != 2 {
		t.Fatal("the held-back trailing event must be superseded, not published late")
	}
}

func TestProgressCoalesceRemovalFlushesImmediately(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "t", Title: ptr("T")})
	h.clock.Advance(10 * time.Millisecond)
	h.up("a", TaskUpdate{ID: "t", Percent: ptr(50.0)}) // held back

	h.store.RemoveAgent("a")

	events := h.taskEvents("a")
	if len(events) != 2 || events[1].Task != nil {
		t.Fatalf("removal must publish null at once: %d events", len(events))
	}
	h.clock.Advance(time.Second)
	if len(h.taskEvents("a")) != 2 {
		t.Fatal("a pending trailing event must not resurrect a removed task")
	}
	if h.clock.pendingTimers() != 0 {
		t.Fatalf("pending timers = %d", h.clock.pendingTimers())
	}
}

func TestProgressCoalesceIsPerTask(t *testing.T) {
	h := newProgressHarness(t)
	h.up("a", TaskUpdate{ID: "x", Title: ptr("T")})
	h.up("a", TaskUpdate{ID: "y", Title: ptr("T")})
	h.up("b", TaskUpdate{ID: "x", Title: ptr("T")})
	if len(h.pub.events) != 3 {
		t.Fatalf("each task has its own window, published %d", len(h.pub.events))
	}
}
