package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

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

// --- Failure paths through the real handler. ---
//
// The isolated publishAgentMessage tests above prove the payload is safe; these
// prove the handler only reaches it on an actual delivery. A spurious
// agent_message tells a consumer two agents are talking when nothing was ever
// delivered — the exact thing that would strand kiosk characters in a
// conference room.

// TestHandlerPublishesNothingWhenSendKeysFails covers the live fast path: a
// failing tmux send-keys must 500 and announce nothing.
func TestHandlerPublishesNothingWhenSendKeysFails(t *testing.T) {
	s, _ := newTestServer(t)
	pub := &recordingPublisher{}
	s.publisher = pub

	s.execCommand = func(name string, args ...string) *exec.Cmd {
		if argsContain(args, "send-keys") {
			return exec.Command("false") // delivery fails
		}
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"never lands","from":"chronicle"}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("expected a failure status, got %d", w.Code)
	}
	if msgs := pub.messages(); len(msgs) != 0 {
		t.Fatalf("published %d agent_message events for a failed send: %+v", len(msgs), msgs)
	}
}

// TestHandlerPublishesNothingForUnknownTarget: no agent, no delivery, no event.
func TestHandlerPublishesNothingForUnknownTarget(t *testing.T) {
	s, _ := newTestServer(t)
	pub := &recordingPublisher{}
	s.publisher = pub

	body := strings.NewReader(`{"text":"hello?","from":"chronicle"}`)
	req := httptest.NewRequest("POST", "/web/agent/ghost/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if msgs := pub.messages(); len(msgs) != 0 {
		t.Fatalf("published for an unknown target: %+v", msgs)
	}
}

// TestHandlerPublishesOnSuccessfulSend is the positive control for the two
// tests above: the same handler, the same seams, a delivery that works.
func TestHandlerPublishesOnSuccessfulSend(t *testing.T) {
	s, _ := newTestServer(t)
	pub := &recordingPublisher{}
	s.publisher = pub

	oldPoll := messageInputPoll
	messageInputPoll = time.Millisecond
	defer func() { messageInputPoll = oldPoll }()

	s.execCommand = func(name string, args ...string) *exec.Cmd {
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ ship it")
		}
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"ship it","from":"chronicle"}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	msgs := pub.messages()
	if len(msgs) != 1 {
		t.Fatalf("published %d events, want exactly 1: %+v", len(msgs), msgs)
	}
	if msgs[0].From != "chronicle" || msgs[0].To != "assistant" {
		t.Errorf("payload = %+v", msgs[0])
	}
	// The body must not have leaked in via any field.
	blob, _ := json.Marshal(msgs[0])
	if strings.Contains(string(blob), "ship it") {
		t.Errorf("message body leaked into the event: %s", blob)
	}
}

// TestHandlerPublishesOnlyAfterAsyncDeliverySucceeds: the suspended-resume
// path answers 202 before delivering. Announcing at accept time would report a
// conversation that never happened when the cold-boot injection later fails.
func TestHandlerPublishesOnlyAfterAsyncDeliverySucceeds(t *testing.T) {
	tests := []struct {
		name        string
		injectErr   error
		wantPublish bool
	}{
		{"delivery succeeds", nil, true},
		{"delivery fails after 202", errors.New("cold boot never became ready"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, svc := newTestServerWithAgents(t)
			pub := &recordingPublisher{}
			s.publisher = pub
			svc.wakeableNames = map[string]bool{"suspended-worker": true}

			done := make(chan struct{})
			s.injectPrompt = func(ctx context.Context, session, body string) error {
				defer close(done)
				return tt.injectErr
			}
			s.execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("true") }

			body := strings.NewReader(`{"text":"wake up","from":"chronicle"}`)
			req := httptest.NewRequest("POST", "/web/agent/suspended-worker/message", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, req)

			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("injectPrompt was never called")
			}
			// The publish happens just after inject returns, inside the same
			// goroutine; poll briefly rather than sleeping a fixed amount.
			deadline := time.After(2 * time.Second)
			for {
				got := len(pub.messages())
				if tt.wantPublish && got == 1 {
					return
				}
				if !tt.wantPublish && got > 0 {
					t.Fatalf("published %d events for a failed delivery", got)
				}
				select {
				case <-deadline:
					if tt.wantPublish {
						t.Fatal("no agent_message published for a successful delivery")
					}
					return // stayed silent, as required
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
	}
}
