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

func postHook(t *testing.T, s *Server, agentName, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/"+agentName+"/hook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w.Code
}

func TestAgentHookTransitions(t *testing.T) {
	for _, tc := range []struct {
		name, agent, body string
		want              observe.AttentionState // "" = no transition
	}{
		{"prompt submit", "leo-a", `{"hook_event_name":"UserPromptSubmit","session_id":"sid-a"}`, observe.AttentionWorking},
		{"stop", "leo-a", `{"hook_event_name":"Stop","session_id":"sid-a"}`, observe.AttentionFinished},
		{"codex interrupt", "leo-a", `{"hook_event_name":"Interrupt","session_id":"sid-a"}`, observe.AttentionFinished},
		{"permission prompt", "leo-a", `{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"sid-a"}`, observe.AttentionNeedsInput},
		{"elicitation", "leo-a", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`, observe.AttentionNeedsInput},
		{"idle notification", "leo-a", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, ""},
		{"session end", "leo-a", `{"hook_event_name":"SessionEnd","session_id":"sid-a"}`, ""},
		{"unrecognized event", "leo-a", `{"hook_event_name":"PreToolUse"}`, ""},
		{"unknown agent", "leo-ghost", `{"hook_event_name":"Stop"}`, ""},
		{"foreign session", "leo-a", `{"hook_event_name":"Stop","session_id":"child-sid"}`, ""},
		{"agent without known session", "leo-b", `{"hook_event_name":"Stop","session_id":"any"}`, observe.AttentionFinished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, svc := newTestServerWithAgents(t)
			svc.handles = map[string]resolvedHandle{
				"leo-a": {harnessName: "claude", handle: harness.SessionHandle{Name: "leo-a", IDs: staticIDs("sid-a")}},
				"leo-b": {harnessName: "codex", handle: harness.SessionHandle{Name: "leo-b", IDs: staticIDs("")}},
			}
			s.attention = observe.NewAttentionStore(nil)

			code := postHook(t, s, tc.agent, tc.body)

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

func TestAgentHookRejectsInvalidJSON(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.attention = observe.NewAttentionStore(nil)

	if code := postHook(t, s, "leo-a", `not json`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestAgentHookAuth(t *testing.T) {
	s := newTokenSplitServer(t)

	if w := requestAs(t, s, http.MethodPost, "/api/agent/leo-a/hook", "", `{}`); w.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", w.Code)
	}
	if w := requestAs(t, s, http.MethodPost, "/api/agent/leo-a/hook", testAgentToken, `{}`); w.Code != http.StatusOK {
		t.Errorf("agent token = %d, want 200", w.Code)
	}
}
