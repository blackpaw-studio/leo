package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/chat"
)

const testPostToken = "test-post-token"

// newClientChatServer is a token-split server with chat wired and a post-only
// client (pipe, may post to pipe-*) beside a message-only one (docker-scout).
// Its turn-injection seams fail the test: a client post must never reach them.
func newClientChatServer(t *testing.T) chatServer {
	t.Helper()
	cs := newChatServer(t)
	cs.svc.records = []agent.Record{{Name: "pipe-cam", Workspace: "/ws"}, {Name: "other", Workspace: "/ws"}}
	cs.s.SetClients([]ClientPolicy{
		{Name: "pipe", Token: testPostToken, CanPost: []string{"pipe-*"}},
		{Name: "docker-scout", Token: testClientToken, CanMessage: []string{"pipe-cam"}},
	})
	cs.s.injectPrompt = func(context.Context, string, string) error {
		t.Error("a client post injected a prompt")
		return nil
	}
	cs.s.deliverPeer = func(context.Context, string, string) error {
		t.Error("a client post delivered to a peer")
		return nil
	}
	return cs
}

func clientPost(t *testing.T, s *Server, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return chatRequest(t, s, http.MethodPost, path, token, string(b))
}

func TestClientChatPostLandsWithSenderAndWakesNobody(t *testing.T) {
	cs := newClientChatServer(t)
	w := clientPost(t, cs.s, "/web/agent/pipe-cam/chat/post", testPostToken, map[string]string{"from": "pipe#run_1", "text": "cam offline\nsecond line\twith tab"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var data struct {
		ID string `json:"id"`
	}
	decodeChatData(t, w, &data)
	if data.ID == "" {
		t.Fatalf("no id in %s", w.Body.String())
	}

	h := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/pipe-cam/messages", testAPIToken, "")
	var page chat.PageResult
	decodeChatData(t, h, &page)
	msgs := page.Messages
	if len(msgs) != 1 || msgs[0].ID != data.ID || msgs[0].Sender != "client:pipe" || msgs[0].Direction != chat.DirectionOut {
		t.Fatalf("history = %+v", msgs)
	}
	if msgs[0].Text != "cam offline\nsecond line\twith tab" {
		t.Errorf("text = %q, want newlines and tabs kept", msgs[0].Text)
	}
	if !strings.Contains(h.Body.String(), `"sender":"client:pipe"`) {
		t.Errorf("history JSON lacks the sender: %s", h.Body.String())
	}
	if len(cs.deliver.prompts) != 0 {
		t.Errorf("a client post delivered %v", cs.deliver.prompts)
	}
}

func TestClientChatPostStripsControlCharactersButKeepsNewlines(t *testing.T) {
	cs := newClientChatServer(t)
	w := clientPost(t, cs.s, "/web/agent/pipe-cam/chat/post", testPostToken, map[string]string{"from": "pipe", "text": "a\x1b[31mred\x00\r\nb"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var page chat.PageResult
	decodeChatData(t, chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/pipe-cam/messages", testAPIToken, ""), &page)
	if got := page.Messages[0].Text; got != "a[31mred\nb" {
		t.Errorf("text = %q, want control characters stripped and the line break kept", got)
	}
}

func TestClientChatPostDenials(t *testing.T) {
	cs := newClientChatServer(t)
	ok := map[string]string{"from": "pipe", "text": "hi"}

	tests := []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"target outside can_post", http.MethodPost, "/web/agent/other/chat/post", testPostToken, http.StatusForbidden},
		{"can_post never grants message", http.MethodPost, "/web/agent/pipe-cam/message", testPostToken, http.StatusForbidden},
		{"can_message never grants chat/post", http.MethodPost, "/web/agent/pipe-cam/chat/post", testClientToken, http.StatusForbidden},
		{"api route", http.MethodPost, "/api/v1/state", testPostToken, http.StatusForbidden},
		{"api agent spawn", http.MethodPost, "/api/agent/spawn", testPostToken, http.StatusForbidden},
		{"other agent verb", http.MethodPost, "/web/agent/pipe-cam/interrupt", testPostToken, http.StatusForbidden},
		{"GET on the route", http.MethodGet, "/web/agent/pipe-cam/chat/post", testPostToken, http.StatusForbidden},
		{"nested target", http.MethodPost, "/web/agent/pipe-cam/x/chat/post", testPostToken, http.StatusForbidden},
		{"unknown token", http.MethodPost, "/web/agent/pipe-cam/chat/post", "nope", http.StatusUnauthorized},
		{"operator token has no such route", http.MethodPost, "/web/agent/pipe-cam/chat/post", testAPIToken, http.StatusNotFound},
		{"agent token is refused off its callable paths", http.MethodPost, "/web/agent/pipe-cam/chat/post", testAgentToken, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if tt.method == http.MethodGet {
				w = chatRequest(t, cs.s, tt.method, tt.path, tt.token, "")
			} else {
				w = clientPost(t, cs.s, tt.path, tt.token, ok)
			}
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
	if page, _ := cs.s.chat.Messages("pipe-cam", chat.Page{}); len(page.Messages) != 0 {
		t.Errorf("denied requests stored messages: %+v", page.Messages)
	}
}

func TestClientChatPostValidation(t *testing.T) {
	cs := newClientChatServer(t)
	path := "/web/agent/pipe-cam/chat/post"

	for name, body := range map[string]map[string]string{
		"bad from":       {"from": "docker-scout", "text": "hi"},
		"missing from":   {"text": "hi"},
		"empty text":     {"from": "pipe", "text": ""},
		"whitespace":     {"from": "pipe", "text": " \n "},
		"oversized text": {"from": "pipe", "text": strings.Repeat("x", chat.MaxTextBytes+1)},
	} {
		if w := clientPost(t, cs.s, path, testPostToken, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}
	if w := chatRequest(t, cs.s, http.MethodPost, path, testPostToken, "{not json"); w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: status = %d, want 400", w.Code)
	}
}

func TestClientChatPostToAnUnknownInScopeAgentIs404(t *testing.T) {
	cs := newClientChatServer(t)
	w := clientPost(t, cs.s, "/web/agent/pipe-ghost/chat/post", testPostToken, map[string]string{"from": "pipe", "text": "hi"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestClientChatPostWithoutChatIs503(t *testing.T) {
	cs := newClientChatServer(t)
	cs.s.chat = nil
	w := clientPost(t, cs.s, "/web/agent/pipe-cam/chat/post", testPostToken, map[string]string{"from": "pipe", "text": "hi"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
