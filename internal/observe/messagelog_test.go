package observe

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// capturingPublisher records everything forwarded to it.
type capturingPublisher struct {
	mu     sync.Mutex
	events []Event
}

func (p *capturingPublisher) Publish(ev Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
}

func (p *capturingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func msgEvent(from, to string, at time.Time) Event {
	p := &AgentMessagePayload{From: from, To: to}
	p.stamp(0, at)
	return Event{Type: EventAgentMessage, Payload: p}
}

// TestMessageLogRecordsAndForwards: the log sits in front of the bus, so it
// must record message events AND pass every event through untouched.
func TestMessageLogRecordsAndForwards(t *testing.T) {
	next := &capturingPublisher{}
	log := NewMessageLog(next, 0)
	now := time.Now()

	log.Publish(msgEvent("chronicle", "plex", now))
	log.Publish(Event{Type: EventAgentActivity, Payload: &AgentActivityPayload{Agent: "other"}})

	if next.count() != 2 {
		t.Fatalf("forwarded %d events, want both", next.count())
	}
	got := log.Recent(0, now)
	if len(got) != 1 {
		t.Fatalf("Recent() = %+v, want only the message event", got)
	}
	if got[0].From != "chronicle" || got[0].To != "plex" {
		t.Errorf("recorded pair = %+v", got[0])
	}
	if !got[0].At.Equal(now) {
		t.Errorf("At = %v, want the event's stamp %v", got[0].At, now)
	}
}

// TestMessageLogNilNextIsValid mirrors RunLog: record-only is a supported
// configuration, not a crash.
func TestMessageLogNilNextIsValid(t *testing.T) {
	log := NewMessageLog(nil, 0)
	now := time.Now()
	log.Publish(msgEvent("a", "b", now))
	if len(log.Recent(0, now)) != 1 {
		t.Fatal("record-only log did not record")
	}
}

// TestMessageLogNewestLast: the kiosk replays pairs in the order they
// happened, so ordering is part of the contract.
func TestMessageLogNewestLast(t *testing.T) {
	log := NewMessageLog(nil, 0)
	base := time.Now()
	for i, pair := range [][2]string{{"a", "b"}, {"c", "d"}, {"e", "f"}} {
		log.Publish(msgEvent(pair[0], pair[1], base.Add(time.Duration(i)*time.Second)))
	}
	got := log.Recent(0, base.Add(3*time.Second))
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got[0].From != "a" || got[2].From != "e" {
		t.Errorf("wrong order (want oldest first, newest last): %+v", got)
	}
}

// TestMessageLogTrimsToCapacity keeps an endlessly chatty pair from growing
// the snapshot without bound.
func TestMessageLogTrimsToCapacity(t *testing.T) {
	log := NewMessageLog(nil, 3)
	base := time.Now()
	for i := 0; i < 10; i++ {
		log.Publish(msgEvent("a", "b", base.Add(time.Duration(i)*time.Second)))
	}
	got := log.Recent(0, base.Add(10*time.Second))
	if len(got) != 3 {
		t.Fatalf("got %d entries, want the capacity of 3", len(got))
	}
	// The survivors must be the NEWEST three, not the first three seen.
	if !got[2].At.Equal(base.Add(9 * time.Second)) {
		t.Errorf("newest entry = %v, want the last published", got[2].At)
	}
}

// TestMessageLogDropsStaleEntries: a pair that talked an hour ago must not
// send kiosk characters into a conference room on reconnect.
func TestMessageLogDropsStaleEntries(t *testing.T) {
	log := NewMessageLog(nil, 0)
	now := time.Now()
	log.Publish(msgEvent("old", "pair", now.Add(-time.Hour)))
	log.Publish(msgEvent("fresh", "pair", now.Add(-time.Minute)))

	got := log.Recent(0, now)
	if len(got) != 1 || got[0].From != "fresh" {
		t.Fatalf("Recent() = %+v, want only the fresh pair", got)
	}
}

// TestMessageLogRecentLimit lets a caller ask for fewer than are held.
func TestMessageLogRecentLimit(t *testing.T) {
	log := NewMessageLog(nil, 0)
	base := time.Now()
	for i := 0; i < 5; i++ {
		log.Publish(msgEvent("a", "b", base.Add(time.Duration(i)*time.Second)))
	}
	got := log.Recent(2, base.Add(5*time.Second))
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	// Limiting keeps the newest, not the oldest.
	if !got[1].At.Equal(base.Add(4 * time.Second)) {
		t.Errorf("limited slice kept the wrong end: %+v", got)
	}
}

// TestMessageLogNeverCarriesContent is the privacy pin. The payload type has
// no content field by construction; this fails loudly if one is ever added.
func TestMessageLogNeverCarriesContent(t *testing.T) {
	log := NewMessageLog(nil, 0)
	now := time.Now()
	log.Publish(msgEvent("a", "b", now))

	got := log.Recent(0, now)[0]
	if got.From != "a" || got.To != "b" || got.At.IsZero() {
		t.Fatalf("unexpected entry shape: %+v", got)
	}
	// AgentMessage must remain exactly {From, To, At} — three fields, no body.
	if fields := reflectFieldNames(got); len(fields) != 3 {
		t.Fatalf("AgentMessage has %v; message content must never be recorded", fields)
	}
}

// TestMessageLogConcurrentPublish runs under -race in CI.
func TestMessageLogConcurrentPublish(t *testing.T) {
	log := NewMessageLog(&capturingPublisher{}, 0)
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Publish(msgEvent("a", "b", now))
			log.Recent(0, now)
		}()
	}
	wg.Wait()
}

// reflectFieldNames lists a struct's JSON-visible field names.
func reflectFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

// TestMessageLogStampsUnstampedPayloads guards the production path: the log
// sits in front of the bus, so payloads arrive with a zero Meta.At. Recording
// that verbatim would date every entry to the zero time and have Recent's age
// filter drop it — the snapshot would silently always be empty.
func TestMessageLogStampsUnstampedPayloads(t *testing.T) {
	log := NewMessageLog(nil, 0)

	// Exactly what web.publishAgentMessage constructs: no stamp.
	log.Publish(Event{
		Type:    EventAgentMessage,
		Payload: &AgentMessagePayload{From: "a", To: "b"},
	})

	got := log.Recent(0, time.Now())
	if len(got) != 1 {
		t.Fatalf("Recent() = %+v, want the unstamped entry to survive", got)
	}
	if got[0].At.IsZero() {
		t.Error("entry recorded with a zero timestamp")
	}
}
