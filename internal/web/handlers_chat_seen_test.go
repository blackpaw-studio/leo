package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// nextEvent returns the next published event of type t, skipping others.
func nextEvent(t *testing.T, events chan observe.Event, typ observe.EventType) observe.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event published", typ)
		}
	}
}

func TestAgentSeenRoutesTakeOperatorOrDeviceTokensOnly(t *testing.T) {
	cs := newChatServer(t)
	store := devices.NewStore(filepath.Join(t.TempDir(), "state"), nil)
	cs.s.devices = store
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	_, deviceToken, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/agents/assistant/seen", ""},
		{http.MethodPut, "/api/v1/agents/seen", `{"agents":["assistant"]}`},
		{http.MethodGet, "/api/v1/agents/seen", ""},
	} {
		for _, tc := range []struct {
			token string
			want  int
		}{{"", http.StatusUnauthorized}, {testAgentToken, http.StatusForbidden}, {testAPIToken, http.StatusOK}, {deviceToken, http.StatusOK}} {
			if w := chatRequest(t, cs.s, route.method, route.path, tc.token, route.body); w.Code != tc.want {
				t.Errorf("%s %s token %q: status %d, want %d (%s)", route.method, route.path, tc.token, w.Code, tc.want, w.Body)
			}
		}
	}
}

func TestAgentSeenRoundTripsOverHTTPAndPublishesAnEvent(t *testing.T) {
	cs := newChatServer(t)

	w := chatRequest(t, cs.s, http.MethodPut, "/api/v1/agents/assistant/seen", testAPIToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT seen = %d %s", w.Code, w.Body)
	}
	var one struct {
		Agent  string    `json:"agent"`
		SeenAt time.Time `json:"seen_at"`
	}
	decodeChatData(t, w, &one)
	if one.Agent != "assistant" || one.SeenAt.IsZero() {
		t.Fatalf("PUT body = %+v, want agent and seen_at", one)
	}
	ev := nextEvent(t, cs.events, chat.EventAgentSeen).Payload.(*chat.AgentSeenPayload)
	if ev.Agent != "assistant" || !ev.SeenAt.Equal(one.SeenAt) {
		t.Fatalf("event = %+v, want assistant at %v", ev, one.SeenAt)
	}

	w = chatRequest(t, cs.s, http.MethodGet, "/api/v1/agents/seen", testAPIToken, "")
	var all map[string]string
	decodeChatData(t, w, &all)
	if got, err := time.Parse(time.RFC3339Nano, all["assistant"]); err != nil || !got.Equal(one.SeenAt) || len(all) != 1 {
		t.Fatalf("GET seen = %v (%v), want assistant at %v as RFC 3339", all, err, one.SeenAt)
	}
}

func TestAgentSeenBatchMarksEveryNamedAgent(t *testing.T) {
	cs := newChatServer(t)

	w := chatRequest(t, cs.s, http.MethodPut, "/api/v1/agents/seen", testAPIToken, `{"agents":["assistant","web"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", w.Code, w.Body)
	}
	var got map[string]string
	decodeChatData(t, w, &got)
	if len(got) != 2 || got["assistant"] == "" || got["web"] == "" {
		t.Fatalf("batch body = %v, want both agents", got)
	}
	nextEvent(t, cs.events, chat.EventAgentSeen)
	nextEvent(t, cs.events, chat.EventAgentSeen)
}

func TestAgentSeenRejectsBadInput(t *testing.T) {
	cs := newChatServer(t)
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"single with a bad name", http.MethodPut, "/api/v1/agents/Bad_Name/seen", "", http.StatusBadRequest},
		{"batch with a bad name", http.MethodPut, "/api/v1/agents/seen", `{"agents":["assistant","../x"]}`, http.StatusBadRequest},
		{"batch with no agents", http.MethodPut, "/api/v1/agents/seen", `{"agents":[]}`, http.StatusBadRequest},
		{"batch with unknown fields only", http.MethodPut, "/api/v1/agents/seen", `{"names":["assistant"]}`, http.StatusBadRequest},
		{"batch that is not JSON", http.MethodPut, "/api/v1/agents/seen", `nope`, http.StatusBadRequest},
		{"batch with trailing data", http.MethodPut, "/api/v1/agents/seen", `{"agents":["assistant"]} {}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := chatRequest(t, cs.s, tc.method, tc.path, testAPIToken, tc.body); w.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, tc.want, w.Body)
			}
		})
	}
	if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/agents/seen", testAPIToken, ""); !strings.Contains(w.Body.String(), `"data":{}`) {
		t.Fatalf("a rejected write left state behind: %s", w.Body)
	}
}

func TestSeenRoutesAnswer503WithoutChat(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/agents/assistant/seen", ""},
		{http.MethodGet, "/api/v1/agents/seen", ""},
		{http.MethodGet, "/api/v1/chat/asks?state=open", ""},
	} {
		if w := chatRequest(t, s, tc.method, tc.path, testAPIToken, tc.body); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, w.Code)
		}
	}
}

func TestChatOpenAsksListsOpenAsksAndPermissionsOldestFirst(t *testing.T) {
	cs := newChatServer(t)
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for i, m := range []chat.Message{
		{ID: "01A", Kind: chat.KindAsk, State: chat.StateOpen},
		{ID: "01B", Kind: chat.KindAsk, State: chat.StateAnswered},
		{ID: "01C", Kind: chat.KindPermission, State: chat.StateOpen},
	} {
		m.Agent, m.Direction, m.Text, m.TS = "assistant", chat.DirectionOut, "q", at.Add(time.Duration(i)*time.Second)
		if err := cs.store.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/asks?state=open", testAPIToken, "")

	if w.Code != http.StatusOK {
		t.Fatalf("asks = %d %s", w.Code, w.Body)
	}
	var got struct {
		Messages []chat.Message `json:"messages"`
	}
	decodeChatData(t, w, &got)
	if len(got.Messages) != 2 || got.Messages[0].ID != "01A" || got.Messages[1].ID != "01C" || got.Messages[0].Agent != "assistant" {
		t.Fatalf("asks = %+v, want the two open ones oldest first", got.Messages)
	}
	if !strings.Contains(w.Body.String(), `"state":"open"`) || !strings.Contains(w.Body.String(), `"kind":"permission"`) {
		t.Fatalf("body = %s, want the chat message JSON shape", w.Body)
	}
}

func TestChatOpenAsksWithNoneOpenIsAnEmptyList(t *testing.T) {
	cs := newChatServer(t)
	w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/asks?state=open", testAPIToken, "")
	if !strings.Contains(w.Body.String(), `"data":{"messages":[]}`) {
		t.Fatalf("body = %s, want an empty messages array, not null", w.Body)
	}
}

func TestChatOpenAsksRequiresTheOpenStateFilter(t *testing.T) {
	cs := newChatServer(t)
	for _, q := range []string{"", "?state=answered", "?state="} {
		if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/asks"+q, testAPIToken, ""); w.Code != http.StatusBadRequest {
			t.Errorf("asks%s = %d, want 400 (only state=open is served)", q, w.Code)
		}
	}
}

// The Mac app reaches its own daemon over the unix socket at the same paths
// as TCP, with no token (the 0600 socket is the auth), as for the chat routes.
func TestSocketServesSeenAndOpenAsksWithoutAToken(t *testing.T) {
	cs := newChatServer(t)
	handler := cs.s.ChatHandler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	if w := do(http.MethodPut, "/api/v1/agents/assistant/seen", ""); w.Code != http.StatusOK {
		t.Fatalf("socket PUT seen = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/api/v1/agents/seen", `{"agents":["web"]}`); w.Code != http.StatusOK {
		t.Fatalf("socket batch = %d %s", w.Code, w.Body)
	}
	w := do(http.MethodGet, "/api/v1/agents/seen", "")
	var all map[string]string
	decodeChatData(t, w, &all)
	if len(all) != 2 || all["assistant"] == "" || all["web"] == "" {
		t.Fatalf("socket GET seen = %v, want both agents", all)
	}
	if w := do(http.MethodGet, "/api/v1/chat/asks?state=open", ""); w.Code != http.StatusOK {
		t.Fatalf("socket asks = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/api/v1/agents/Bad_Name/seen", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("socket bad name = %d, want 400", w.Code)
	}
	// A write over the socket reaches the TCP side's apps through the bus.
	nextEvent(t, cs.events, chat.EventAgentSeen)
}
