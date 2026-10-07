package observe

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// B-051 fixture: a scripted bridge trace in which the main loop's turn ends
// while a child (native subagent or leo dispatch) is still running. The
// published SSE stream must hold the agent at working until the last child
// ends, then publish finished exactly once — never working → finished →
// working.

// syncRecorder is a recordingPublisher safe for timer goroutines.
type syncRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *syncRecorder) Publish(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *syncRecorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *syncRecorder) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

// attentionTrace is the agent's published attention states in order, with
// consecutive repeats folded: what a client's status indicator shows.
func attentionTrace(events []Event, agent string) []AttentionState {
	var out []AttentionState
	for _, ev := range events {
		p, ok := ev.Payload.(*AgentActivityPayload)
		if !ok || p.Agent != agent || p.Attention == nil {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != p.Attention.State {
			out = append(out, p.Attention.State)
		}
	}
	return out
}

func eventTypes(events []Event) []EventType {
	out := make([]EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

// b051Harness is a bridged agent "alice" (bridge key "alice-key") with a
// tracked attention source, as the supervisor leaves it at launch.
type b051Harness struct {
	pub   *syncRecorder
	store *AttentionStore
	feed  *BridgeFeed
	clock *fakeClock
	t0    time.Time
}

func newB051Harness(t *testing.T) *b051Harness {
	t.Helper()
	pub := &syncRecorder{}
	store := NewAttentionStore(pub)
	store.BindBridgeKey("alice-key", "alice-key-launch", "alice")
	store.Set("alice", AttentionUnknown)
	clock := newFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	feed := NewBridgeFeed(staticKeys{"alice-key": "alice"}, pub, WithFeedAttention(store), WithFeedClock(clock))
	pub.reset()
	return &b051Harness{pub: pub, store: store, feed: feed, clock: clock, t0: clock.Now()}
}

func (h *b051Harness) send(ev bridge.Event) {
	ev.Agent, ev.LaunchID = "alice-key", "alice-key-launch"
	if ev.Gen == 0 {
		ev.Gen = 1
	}
	if ev.SessionID == "" {
		ev.SessionID = "s1"
	}
	h.clock.Advance(100 * time.Millisecond)
	ev.At = h.clock.Now()
	h.feed.OnBridgeEvent(ev)
}

func (h *b051Harness) requireAttention(t *testing.T, state AttentionState, outstanding *Outstanding) {
	t.Helper()
	got, ok := h.store.Get("alice")
	if !ok || got.State != state {
		t.Fatalf("attention = %+v (tracked %v); want %s", got, ok, state)
	}
	switch {
	case outstanding == nil && got.Outstanding != nil:
		t.Fatalf("outstanding = %+v; want absent", *got.Outstanding)
	case outstanding != nil && (got.Outstanding == nil || *got.Outstanding != *outstanding):
		t.Fatalf("outstanding = %+v; want %+v", got.Outstanding, *outstanding)
	}
}

func (h *b051Harness) requireNoFlicker(t *testing.T) {
	t.Helper()
	events := h.pub.snapshot()
	trace := attentionTrace(events, "alice")
	want := []AttentionState{AttentionWorking, AttentionFinished}
	if !slices.Equal(trace, want) {
		t.Fatalf("attention trace = %v; want %v\nevents: %v", trace, want, eventTypes(events))
	}
	completed := slices.IndexFunc(events, func(ev Event) bool { return ev.Type == EventAgentTurnCompleted })
	if completed < 0 {
		t.Fatalf("no agent_turn_completed in %v", eventTypes(events))
	}
}

func TestB051BackgroundSubagentHoldsWorkingUntilItStops(t *testing.T) {
	h := newB051Harness(t)

	h.send(bridge.Event{Name: bridge.ReportHello})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 1}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Message: "spawned a helper"})

	h.requireAttention(t, AttentionWorking, &Outstanding{Subagents: 1})

	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 0}})

	h.requireAttention(t, AttentionFinished, nil)
	h.requireNoFlicker(t)
}

func TestB051OutstandingDispatchHoldsWorkingUntilItEnds(t *testing.T) {
	h := newB051Harness(t)

	h.send(bridge.Event{Name: bridge.ReportHello})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	setDispatches(h.store, "alice-key-launch", 1)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Message: "dispatched a reviewer"})

	h.requireAttention(t, AttentionWorking, &Outstanding{Dispatches: 1})

	setDispatches(h.store, "alice-key-launch", 0)

	h.requireAttention(t, AttentionFinished, nil)
	h.requireNoFlicker(t)
}

func TestB051StopHookDuringHoldDoesNotFinishEarly(t *testing.T) {
	h := newB051Harness(t)

	h.send(bridge.Event{Name: bridge.ReportHello})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	setDispatches(h.store, "alice-key-launch", 1)
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 1}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1"})
	// The claude Stop hook lands after the bridge's turn.complete.
	h.store.Set("alice", AttentionFinished)
	h.requireAttention(t, AttentionWorking, &Outstanding{Dispatches: 1, Subagents: 1})

	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 0}})
	h.requireAttention(t, AttentionWorking, &Outstanding{Dispatches: 1})

	setDispatches(h.store, "alice-key-launch", 0)
	h.requireAttention(t, AttentionFinished, nil)
	h.requireNoFlicker(t)
}

func TestB051NewTurnDuringHoldCancelsDeferredFinish(t *testing.T) {
	h := newB051Harness(t)

	h.send(bridge.Event{Name: bridge.ReportHello})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 1}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1"})
	// The subagent's result wakes the main loop into a new turn, then ends.
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts2"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 0}})

	h.requireAttention(t, AttentionWorking, nil)

	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc2"})
	h.requireAttention(t, AttentionFinished, nil)
	h.requireNoFlicker(t)
}
