package web

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingPublisher captures published events for assertion.
type recordingPublisher struct {
	mu     sync.Mutex
	events []observe.Event
}

func (p *recordingPublisher) Publish(ev observe.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
}

func (p *recordingPublisher) messages() []*observe.AgentMessagePayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*observe.AgentMessagePayload
	for _, ev := range p.events {
		if ev.Type != observe.EventAgentMessage {
			continue
		}
		if m, ok := ev.Payload.(*observe.AgentMessagePayload); ok {
			out = append(out, m)
		}
	}
	return out
}

// TestPublishAgentMessageCarriesPairOnly is the privacy pin at the publish
// seam: the routed body must never reach the event.
func TestPublishAgentMessageCarriesPairOnly(t *testing.T) {
	pub := &recordingPublisher{}
	s := &Server{publisher: pub}

	s.publishAgentMessage("chronicle", "plex")

	msgs := pub.messages()
	if len(msgs) != 1 {
		t.Fatalf("published %d agent_message events, want 1", len(msgs))
	}
	if msgs[0].From != "chronicle" || msgs[0].To != "plex" {
		t.Errorf("payload = %+v", msgs[0])
	}

	// Serialize and prove no content field exists at all.
	blob, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, banned := range []string{"text", "message", "body", "content"} {
		if _, present := fields[banned]; present {
			t.Errorf("payload carries %q: %s", banned, blob)
		}
	}
}

// TestPublishAgentMessageOmitsUnknownSender: a human messaging from the web UI
// has no agent identity, and leo must not invent one. `from` is absent from
// the JSON so consumers can require it.
func TestPublishAgentMessageOmitsUnknownSender(t *testing.T) {
	pub := &recordingPublisher{}
	s := &Server{publisher: pub}

	s.publishAgentMessage("", "plex")

	msgs := pub.messages()
	if len(msgs) != 1 {
		t.Fatalf("published %d events, want 1", len(msgs))
	}
	if msgs[0].From != "" {
		t.Errorf("From = %q, want empty for a non-agent sender", msgs[0].From)
	}
	blob, _ := json.Marshal(msgs[0])
	if strings.Contains(string(blob), `"from"`) {
		t.Errorf("empty from must be omitted from the wire: %s", blob)
	}
}

// TestPublishAgentMessageNilPublisherIsSafe: the publisher is optional, same
// as every other observability seam on Server.
func TestPublishAgentMessageNilPublisherIsSafe(t *testing.T) {
	s := &Server{}
	s.publishAgentMessage("a", "b") // must not panic
}
