package bridge

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func apply(t *testing.T, h *Hub, agent string, r Report) {
	t.Helper()
	if err := h.Apply(agent, launchOf(t, h, agent), r); err != nil {
		t.Fatalf("Apply(%+v): %v", r, err)
	}
}

func hello(session string) Report {
	return Report{Type: ReportHello, SessionID: session, ClaudeVersion: "2.1.287"}
}

func event(name string) Report { return Report{Type: ReportEvent, Name: name} }

func TestHelloRecordsMetadata(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	_ = mustConnect(t, h, agentA)
	connectedAt := clock.Now()
	clock.Advance(time.Second)
	apply(t, h, agentA, hello("s-1"))

	st := h.State(agentA)
	if !st.Connected || !st.ConnectedAt.Equal(connectedAt) {
		t.Fatalf("connection metadata wrong: %+v", st)
	}
	if st.SessionID != "s-1" || st.ClaudeVersion != "2.1.287" || !st.HelloAt.Equal(clock.Now()) {
		t.Fatalf("hello metadata wrong: %+v", st)
	}
}

func TestTurnEventsDriveBusyAndIdle(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	apply(t, h, agentA, hello("s-1"))

	apply(t, h, agentA, event(EventTurnStart))
	if st := h.State(agentA); !st.Busy {
		t.Fatalf("turn.start: want Busy, got %+v", st)
	}

	clock.Advance(time.Minute)
	usage := json.RawMessage(`{"context":{"used":42}}`)
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnComplete, Usage: usage})
	st := h.State(agentA)
	if st.Busy {
		t.Fatalf("turn.complete: want idle, got %+v", st)
	}
	if !st.LastTurnComplete.Equal(clock.Now()) {
		t.Fatalf("LastTurnComplete=%v, want %v", st.LastTurnComplete, clock.Now())
	}
	if string(st.Usage) != string(usage) {
		t.Fatalf("Usage=%s, want %s", st.Usage, usage)
	}

	// A completion without usage keeps the last known usage.
	apply(t, h, agentA, event(EventTurnStart))
	apply(t, h, agentA, event(EventTurnComplete))
	if got := h.State(agentA).Usage; string(got) != string(usage) {
		t.Fatalf("usage dropped by a usage-less completion: %s", got)
	}
}

func TestStateUsageIsACopy(t *testing.T) {
	h := newTestHub(newFakeClock())
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnComplete, Usage: json.RawMessage(`{"a":1}`)})
	st := h.State(agentA)
	st.Usage[2] = 'X'
	if got := h.State(agentA).Usage; string(got) != `{"a":1}` {
		t.Fatalf("caller mutation leaked into hub state: %s", got)
	}
}

func TestSessionEndClearsBusy(t *testing.T) {
	h := newTestHub(newFakeClock())
	apply(t, h, agentA, event(EventTurnStart))
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventSessionEnd, Reason: "prompt_input_exit"})
	if st := h.State(agentA); st.Busy {
		t.Fatalf("session.end: want idle, got %+v", st)
	}
}

func TestHelloForNewSessionResetsBusy(t *testing.T) {
	h := newTestHub(newFakeClock())
	apply(t, h, agentA, hello("s-1"))
	apply(t, h, agentA, event(EventTurnStart))

	// A mod reload re-says hello for the same session; the turn is still on.
	apply(t, h, agentA, hello("s-1"))
	if !h.State(agentA).Busy {
		t.Fatal("hello for the same session must keep the running turn")
	}
	// A new session cannot have a turn running yet.
	apply(t, h, agentA, hello("s-2"))
	if st := h.State(agentA); st.Busy || st.SessionID != "s-2" {
		t.Fatalf("hello for a new session: want idle on s-2, got %+v", st)
	}
}

func TestSubscriberSeesEventsInOrderWithSession(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	h := newTestHub(clock, func(o *Options) { o.Subscriber = rec })
	apply(t, h, agentA, hello("s-1"))
	apply(t, h, agentA, event(EventTurnStart))
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnComplete, Usage: json.RawMessage(`{"u":1}`)})
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventSessionEnd, Reason: "clear"})
	ack(t, h, agentA, "unrelated") // acks are not events

	got := rec.snapshot()
	want := []string{ReportHello, EventTurnStart, EventTurnComplete, EventSessionEnd}
	if len(got) != len(want) {
		t.Fatalf("got %d events %+v, want %v", len(got), got, want)
	}
	for i, ev := range got {
		if ev.Name != want[i] || ev.Agent != agentA || ev.SessionID != "s-1" || !ev.At.Equal(clock.Now()) {
			t.Fatalf("event %d = %+v, want %s for %s on s-1", i, ev, want[i], agentA)
		}
	}
	if got[0].ClaudeVersion != "2.1.287" {
		t.Fatalf("hello event lost claude_version: %+v", got[0])
	}
	for i, ev := range got {
		if ev.LaunchID != LaunchID(agentA, testLaunch) {
			t.Fatalf("event %d LaunchID = %q; want the reporting launch's", i, ev.LaunchID)
		}
	}
	if string(got[2].Usage) != `{"u":1}` || got[3].Reason != "clear" {
		t.Fatalf("payload fields lost: %+v / %+v", got[2], got[3])
	}
}

// The subscriber runs after the hub lock is released, so it may read hub
// state (as an idle-suspend or consult adapter will) without deadlocking.
func TestSubscriberMayReadHub(t *testing.T) {
	var seen State
	var h *Hub
	h = newTestHub(newFakeClock(), func(o *Options) {
		o.Subscriber = SubscriberFunc(func(ev Event) { seen = h.State(ev.Agent) })
	})
	apply(t, h, agentA, event(EventTurnStart))
	if !seen.Busy {
		t.Fatalf("subscriber read stale state: %+v", seen)
	}
}

func TestApplyRejectsBadInput(t *testing.T) {
	h := newTestHub(newFakeClock())
	if err := h.Apply("", testLaunch, event(EventTurnStart)); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("empty agent err=%v, want ErrInvalidAgent", err)
	}
	if err := h.Apply(agentA, testLaunch, Report{Type: "bogus"}); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("bogus type err=%v, want ErrInvalidReport", err)
	}
	h.Close()
	if err := h.Apply(agentA, testLaunch, event(EventTurnStart)); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed hub err=%v, want ErrClosed", err)
	}
}

// turn.start's prompt and turn.complete's final message reach subscribers
// verbatim; the dispatch adapter returns the final message as the result.
func TestSubscriberSeesPromptAndFinalMessage(t *testing.T) {
	rec := &recorder{}
	h := newTestHub(newFakeClock(), func(o *Options) { o.Subscriber = rec })
	apply(t, h, agentA, hello("s-1"))
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnStart, Prompt: "the brief"})
	apply(t, h, agentA, Report{Type: ReportEvent, Name: EventTurnComplete, Message: "the result", EventID: "turn.complete:t1"})
	got := rec.snapshot()
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[1].Prompt != "the brief" || got[1].Message != "" {
		t.Fatalf("turn.start event = %+v, want prompt only", got[1])
	}
	if got[2].Message != "the result" || got[2].Prompt != "" || got[2].EventID != "turn.complete:t1" {
		t.Fatalf("turn.complete event = %+v, want message only", got[2])
	}
}
