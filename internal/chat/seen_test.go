package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestSeenRoundTripsAndOnlyMovesForward(t *testing.T) {
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	got, err := st.MarkSeen("a", t0)
	if err != nil || !got.Equal(t0) {
		t.Fatalf("MarkSeen = %v, %v; want %v", got, err, t0)
	}
	older, err := st.MarkSeen("a", t0.Add(-time.Hour))
	if err != nil || !older.Equal(t0) {
		t.Fatalf("older MarkSeen = %v, %v; want the existing %v back", older, err, t0)
	}
	newer, _ := st.MarkSeen("a", t0.Add(time.Minute))
	if _, err := st.MarkSeen("b", t0); err != nil {
		t.Fatal(err)
	}

	seen, err := st.Seen()
	if err != nil || len(seen) != 2 || !seen["a"].Equal(t0.Add(time.Minute)) || !newer.Equal(seen["a"]) || !seen["b"].Equal(t0) {
		t.Fatalf("Seen = %v, %v", seen, err)
	}
}

func TestSeenIsEmptyNotNilWhenNothingWasMarked(t *testing.T) {
	seen, err := openTestStore(t).Seen()
	if err != nil || seen == nil || len(seen) != 0 {
		t.Fatalf("Seen = %#v, %v; want an empty map", seen, err)
	}
}

func TestMarkSeenStampsServerTimeAndPublishesEachWrite(t *testing.T) {
	f := newFixture(t)

	got, err := f.svc.MarkSeen([]string{"assistant", "web"})
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock.Now(); !got["assistant"].Equal(want) || !got["web"].Equal(want) {
		t.Fatalf("MarkSeen = %v, want server time %v for both", got, want)
	}
	var published []AgentSeenPayload
	for _, ev := range f.pub.eventsOf(EventAgentSeen) {
		published = append(published, *ev.Payload.(*AgentSeenPayload))
	}
	if len(published) != 2 || published[0].Agent != "assistant" || published[1].Agent != "web" || !published[0].SeenAt.Equal(f.clock.Now()) {
		t.Fatalf("published = %+v, want an agent_seen per agent at server time", published)
	}
}

func TestMarkSeenNeverMovesBackAndPublishesTheEffectiveValue(t *testing.T) {
	f := newFixture(t)
	first, _ := f.svc.MarkSeen([]string{"assistant"})
	f.clock.Advance(-time.Hour) // the daemon clock stepped back

	again, err := f.svc.MarkSeen([]string{"assistant"})

	if err != nil || !again["assistant"].Equal(first["assistant"]) {
		t.Fatalf("MarkSeen = %v, %v; want the earlier %v kept", again, err, first["assistant"])
	}
	evs := f.pub.eventsOf(EventAgentSeen)
	if last := evs[len(evs)-1].Payload.(*AgentSeenPayload); !last.SeenAt.Equal(first["assistant"]) {
		t.Fatalf("published seen_at = %v, want the effective %v", last.SeenAt, first["assistant"])
	}
}

func TestMarkSeenRejectsBadInputWithoutWritingAnything(t *testing.T) {
	f := newFixture(t)
	tooMany := make([]string, MaxSeenBatch+1)
	for i := range tooMany {
		tooMany[i] = "assistant"
	}
	for name, agents := range map[string][]string{
		"empty batch":        {},
		"bad name":           {"assistant", "../etc"},
		"empty name":         {""},
		"oversized batch":    tooMany,
		"name with a slash":  {"a/b"},
		"name with spaces":   {"my agent"},
		"overlong name":      {strings.Repeat("a", 200)},
		"valid then invalid": {"web", "Bad Name"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.MarkSeen(agents); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if seen, _ := f.store.Seen(); len(seen) != 0 {
				t.Fatalf("a rejected batch wrote %v", seen)
			}
			if n := len(f.pub.eventsOf(EventAgentSeen)); n != 0 {
				t.Fatalf("a rejected batch published %d events", n)
			}
		})
	}
}

// Every subscriber of the bus sees the write, as a phone and a Mac do.
func TestAgentSeenReachesEverySubscriberOfTheBus(t *testing.T) {
	bus := observe.NewBus()
	first, unsubFirst, _ := bus.Subscribe(8)
	second, unsubSecond, _ := bus.Subscribe(8)
	defer unsubFirst()
	defer unsubSecond()
	f := newFixture(t)
	f.pub.next = bus

	if _, err := f.svc.MarkSeen([]string{"assistant"}); err != nil {
		t.Fatal(err)
	}

	for name, ch := range map[string]<-chan observe.Event{"first": first, "second": second} {
		select {
		case ev := <-ch:
			p, ok := ev.Payload.(*AgentSeenPayload)
			if ev.Type != "agent_seen" || !ok || p.Agent != "assistant" || p.Seq == 0 {
				t.Fatalf("%s subscriber got %s %+v, want a stamped agent_seen for assistant", name, ev.Type, ev.Payload)
			}
		default:
			t.Fatalf("%s subscriber got no event", name)
		}
	}
}

func TestOpenAsksListsEveryOpenAskAndPermissionOldestFirst(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	insert := func(agent, kind, state string) Message {
		m := msg(ids, clock, agent, DirectionOut, kind, "q")
		m.State = state
		return mustInsert(t, st, m)
	}
	first := insert("web", KindAsk, StateOpen)
	insert("web", KindAsk, StateAnswered)
	insert("assistant", KindAsk, StateExpired)
	insert("assistant", KindText, "")
	insert("assistant", KindPermission, StateAnswered)
	second := insert("assistant", KindPermission, StateOpen)
	third := insert("assistant", KindAsk, StateOpen)

	got, err := st.OpenAsks()

	if err != nil || len(got) != 3 || got[0].ID != first.ID || got[1].ID != second.ID || got[2].ID != third.ID {
		t.Fatalf("OpenAsks = %v, %v; want the three open ones oldest first across agents", ids3(got), err)
	}
}

func TestOpenAsksIsEmptyNotNil(t *testing.T) {
	got, err := openTestStore(t).OpenAsks()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("OpenAsks = %#v, %v; want an empty slice", got, err)
	}
}

func ids3(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID + "/" + m.Kind + "/" + m.State
	}
	return out
}
