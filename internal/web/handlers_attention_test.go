package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// staticIDs is a SessionIDStore holding a fixed id.
type staticIDs string

func (s staticIDs) Get() string { return string(s) }
func (staticIDs) Set(string)    {}
func (staticIDs) Clear()        {}

func postHook(t *testing.T, s *Server, token, payload string) int {
	t.Helper()
	body := `{"token":"` + token + `","payload":` + payload + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/hook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w.Code
}

func newHookServer(t *testing.T) *Server {
	t.Helper()
	s, _, svc := newTestServerWithAgents(t)
	svc.handles = map[string]resolvedHandle{
		"leo-a": {harnessName: "claude", handle: harness.SessionHandle{Name: "leo-a", IDs: staticIDs("sid-a")}},
		"leo-b": {harnessName: "codex", handle: harness.SessionHandle{Name: "leo-b", IDs: staticIDs("")}},
	}
	s.attention = observe.NewAttentionStore(nil)
	s.attention.RegisterToken("tok-a", "leo-a")
	s.attention.RegisterToken("tok-b", "leo-b")
	s.attention.RegisterToken("tok-ghost", "leo-ghost")
	return s
}

func TestAgentHookTransitions(t *testing.T) {
	for _, tc := range []struct {
		name, token, agent, body string
		want                     observe.AttentionState // "" = no transition
	}{
		{"prompt submit", "tok-a", "leo-a", `{"hook_event_name":"UserPromptSubmit","session_id":"sid-a"}`, observe.AttentionWorking},
		{"stop", "tok-a", "leo-a", `{"hook_event_name":"Stop","session_id":"sid-a"}`, observe.AttentionFinished},
		{"codex interrupt", "tok-a", "leo-a", `{"hook_event_name":"Interrupt","session_id":"sid-a"}`, observe.AttentionFinished},
		{"permission prompt", "tok-a", "leo-a", `{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"sid-a"}`, observe.AttentionNeedsInput},
		{"elicitation", "tok-a", "leo-a", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`, observe.AttentionNeedsInput},
		{"idle notification", "tok-a", "leo-a", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, ""},
		{"session end", "tok-a", "leo-a", `{"hook_event_name":"SessionEnd","session_id":"sid-a"}`, ""},
		{"unrecognized event", "tok-a", "leo-a", `{"hook_event_name":"PreToolUse"}`, ""},
		{"post tool use while untracked", "tok-a", "leo-a", `{"hook_event_name":"PostToolUse"}`, ""},
		{"unknown token", "tok-nope", "leo-a", `{"hook_event_name":"Stop"}`, ""},
		{"token of an agent that no longer resolves", "tok-ghost", "leo-ghost", `{"hook_event_name":"Stop"}`, ""},
		{"foreign session", "tok-a", "leo-a", `{"hook_event_name":"Stop","session_id":"child-sid"}`, ""},
		{"agent without known session", "tok-b", "leo-b", `{"hook_event_name":"Stop","session_id":"any"}`, observe.AttentionFinished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHookServer(t)

			code := postHook(t, s, tc.token, tc.body)

			if code < 200 || code > 299 {
				t.Fatalf("status = %d, want 2xx", code)
			}
			att, ok := s.attention.Get(tc.agent)
			if tc.want == "" {
				if ok {
					t.Fatalf("attention = %+v, want no transition", att)
				}
				return
			}
			if !ok || att.State != tc.want || att.Revision != 1 {
				t.Fatalf("attention = %+v, %v; want %s rev 1", att, ok, tc.want)
			}
		})
	}
}

func TestAgentHookPostToolUseClearsOnlyNeedsInput(t *testing.T) {
	for _, tc := range []struct {
		from, want observe.AttentionState
		wantRev    uint64
	}{
		{observe.AttentionNeedsInput, observe.AttentionWorking, 2},
		{observe.AttentionWorking, observe.AttentionWorking, 1},
		{observe.AttentionFinished, observe.AttentionFinished, 1},
		{observe.AttentionUnknown, observe.AttentionUnknown, 1},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			s := newHookServer(t)
			s.attention.Set("leo-a", tc.from)

			postHook(t, s, "tok-a", `{"hook_event_name":"PostToolUse","session_id":"sid-a"}`)

			if att, _ := s.attention.Get("leo-a"); att.State != tc.want || att.Revision != tc.wantRev {
				t.Fatalf("attention = %+v, want %s rev %d", att, tc.want, tc.wantRev)
			}
		})
	}
}

func TestAgentHookFollowsRenameAndIgnoresNameReuse(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	s.attention = observe.NewAttentionStore(nil)
	s.attention.RegisterToken("tok-renamed", "leo-old")
	// Rename leo-old -> leo-new while live, then a new agent takes leo-old.
	s.attention.Move("leo-old", "leo-new")
	s.attention.RegisterToken("tok-fresh", "leo-old")
	svc.handles = map[string]resolvedHandle{
		"leo-new": {harnessName: "claude", handle: harness.SessionHandle{Name: "leo-new"}},
		"leo-old": {harnessName: "claude", handle: harness.SessionHandle{Name: "leo-old"}},
	}

	postHook(t, s, "tok-renamed", `{"hook_event_name":"UserPromptSubmit"}`)

	if att, ok := s.attention.Get("leo-new"); !ok || att.State != observe.AttentionWorking {
		t.Fatalf("renamed agent attention = %+v, %v; want working", att, ok)
	}
	if att, ok := s.attention.Get("leo-old"); ok {
		t.Fatalf("new agent reusing the old name got the renamed agent's hook: %+v", att)
	}
}

func TestAgentHookAfterUnregisterIsNoop(t *testing.T) {
	s := newHookServer(t)
	s.attention.Set("leo-a", observe.AttentionUnknown)
	s.attention.UnregisterAgent("leo-a") // what StopAgent does

	postHook(t, s, "tok-a", `{"hook_event_name":"Stop","session_id":"sid-a"}`)

	if att, _ := s.attention.Get("leo-a"); att != (observe.AgentAttention{State: observe.AttentionUnknown, Revision: 1}) {
		t.Fatalf("attention = %+v, want the stop's unknown untouched", att)
	}
}

func TestAgentHookRejectsInvalidJSON(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.attention = observe.NewAttentionStore(nil)

	if code := postHook(t, s, "tok", `not json`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestAgentHookAuth(t *testing.T) {
	s := newTokenSplitServer(t)

	if w := requestAs(t, s, http.MethodPost, "/api/agent/hook", "", `{}`); w.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", w.Code)
	}
	if w := requestAs(t, s, http.MethodPost, "/api/agent/hook", testAgentToken, `{}`); w.Code != http.StatusOK {
		t.Errorf("agent token = %d, want 200", w.Code)
	}
}
