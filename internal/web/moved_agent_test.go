package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func movedTo(name, host string) error { return &agent.MovedError{Name: name, To: host} }

// TestMessageToMovedAgent409: a message to an agent that moved to another
// host answers 409 agent_moved naming the host, not the 404 an unknown name
// gets, and never wakes it.
func TestMessageToMovedAgent409(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.wakeableNames = map[string]bool{"gone-worker": true}
	svc.movedErrs = map[string]error{"gone-worker": movedTo("gone-worker", "laptop")}

	req := httptest.NewRequest("POST", "/web/agent/gone-worker/message", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"code":"agent_moved"`) || !strings.Contains(w.Body.String(), "laptop") {
		t.Fatalf("body = %s, want code agent_moved naming laptop", w.Body)
	}
	if svc.startCalled {
		t.Fatal("Start called for a moved agent")
	}
}

// TestChatDeliveryToMovedAgentFails: Leo Chat never starts a moved agent and
// says where it went.
func TestChatDeliveryToMovedAgentFails(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.movedErrs = map[string]error{"web": movedTo("web", "laptop")}
	s.injectPrompt = func(context.Context, string, string) error {
		t.Fatal("injected into a moved agent")
		return nil
	}
	starting := false
	_, err := chatDeliverer{s}.Deliver(context.Background(), "web", "[Leo Chat] hi", "chat-1", func() { starting = true })
	if !errors.Is(err, agent.ErrAgentMoved) || !strings.Contains(err.Error(), "laptop") {
		t.Fatalf("Deliver = %v, want ErrAgentMoved naming laptop", err)
	}
	if starting || svc.startCalled {
		t.Fatalf("starting=%v startCalled=%v for a moved agent", starting, svc.startCalled)
	}
}

// TestAgentsPageMovedRowHasBadgeAndNoStart: a moved agent's row carries the
// moved badge, and its Start control posts nowhere.
func TestAgentsPageMovedRowHasBadgeAndNoStart(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-gone", Status: "stopped", MovedTo: "laptop"}}
	data, err := s.buildAgentsData(httptest.NewRequest("GET", "/agents", nil))
	if err != nil {
		t.Fatalf("buildAgentsData: %v", err)
	}
	var buf strings.Builder
	if err := s.templates.ExecuteTemplate(&buf, "agents.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "moved &rarr; laptop") {
		t.Errorf("row lacks the moved badge:\n%s", html)
	}
	if strings.Contains(html, `hx-post="/web/agent/leo-gone/start"`) {
		t.Error("moved row still posts Start")
	}
}

// TestObserveAgentCarriesMovedTo: /api/v1 keeps status "stopped" for a moved
// agent and adds moved_to beside it.
func TestObserveAgentCarriesMovedTo(t *testing.T) {
	a := buildAgent(agent.Record{Name: "leo-gone", Status: "stopped", MovedTo: "laptop"}, nil, agentViews{}, nil)
	if a.Status != "stopped" || a.MovedTo != "laptop" {
		t.Fatalf("observe agent = status %q moved_to %q", a.Status, a.MovedTo)
	}
}
