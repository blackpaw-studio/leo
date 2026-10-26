package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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
		{"stop failure", "tok-a", "leo-a", `{"hook_event_name":"StopFailure","session_id":"sid-a","error":"boom"}`, observe.AttentionFinished},
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

func TestAgentHookStopCarriesTheLastMessageAsPreview(t *testing.T) {
	long := strings.Repeat("word ", 100)
	for _, tc := range []struct {
		name, body, want string
	}{
		{"stop with a message", `{"hook_event_name":"Stop","session_id":"sid-a","last_assistant_message":"Done.\n\nAll green."}`, "Done. All green."},
		{"stop clamps a long message", `{"hook_event_name":"Stop","session_id":"sid-a","last_assistant_message":"` + long + `"}`, observe.ClampPreview(long)},
		{"stop without a message", `{"hook_event_name":"Stop","session_id":"sid-a"}`, ""},
		{"interrupt never previews", `{"hook_event_name":"Interrupt","session_id":"sid-a","last_assistant_message":"nope"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHookServer(t)

			postHook(t, s, "tok-a", tc.body)

			att, ok := s.attention.Get("leo-a")
			if !ok || att.State != observe.AttentionFinished || att.Preview != tc.want {
				t.Fatalf("attention = %+v, %v; want finished with preview %q", att, ok, tc.want)
			}
		})
	}
}

func TestAgentHookForwardsTurnToObserver(t *testing.T) {
	type observed struct {
		agent string
		turn  AgentTurn
	}
	for _, tc := range []struct {
		name, token, body string
		want              []observed
	}{
		{"prompt submit", "tok-b", `{"hook_event_name":"UserPromptSubmit","session_id":"s1","turn_id":"t1","prompt":"reply OK"}`,
			[]observed{{"leo-b", AgentTurn{Event: "UserPromptSubmit", SessionID: "s1", TurnID: "t1", Prompt: "reply OK"}}}},
		{"stop", "tok-b", `{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1","last_assistant_message":"OK"}`,
			[]observed{{"leo-b", AgentTurn{Event: "Stop", SessionID: "s1", TurnID: "t1", LastMessage: "OK"}}}},
		{"stop failure", "tok-b", `{"hook_event_name":"StopFailure","session_id":"s1","error":"ProviderAuthError: bad key"}`,
			[]observed{{"leo-b", AgentTurn{Event: "StopFailure", SessionID: "s1", Error: "ProviderAuthError: bad key"}}}},
		{"stop failure with a structured error", "tok-b", `{"hook_event_name":"StopFailure","session_id":"s1","error":{"type":"rate_limit"}}`,
			[]observed{{"leo-b", AgentTurn{Event: "StopFailure", SessionID: "s1", Error: `{"type":"rate_limit"}`}}}},
		{"unknown token", "tok-nope", `{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1"}`, nil},
		{"foreign session", "tok-a", `{"hook_event_name":"Stop","session_id":"child-sid","turn_id":"t1"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHookServer(t)
			var got []observed
			s.turnObserver = func(agent string, turn AgentTurn) { got = append(got, observed{agent, turn}) }

			postHook(t, s, tc.token, tc.body)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("observed %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAgentHookRaisesNeedsInputWithReason(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       *observe.AttentionReason
	}{
		{"permission request names the tool and its summary",
			`{"hook_event_name":"PermissionRequest","session_id":"sid-a","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}`,
			&observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash", Detail: "rm"}},
		{"elicitation names the server",
			`{"hook_event_name":"Elicitation","session_id":"sid-a","mcp_server_name":"github"}`,
			&observe.AttentionReason{Kind: observe.AttentionReasonElicitation, Detail: "github"}},
		{"a subagent's permission request is not the main loop's",
			`{"hook_event_name":"PermissionRequest","session_id":"sid-a","agent_id":"sub-1","tool_name":"Bash","tool_input":{"command":"ls"}}`, nil},
		{"a question is the bridge's to raise",
			`{"hook_event_name":"PermissionRequest","session_id":"sid-a","tool_name":"AskUserQuestion","tool_input":{"questions":[]}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHookServer(t)

			postHook(t, s, "tok-a", tc.body)

			att, ok := s.attention.Get("leo-a")
			if tc.want == nil {
				if ok {
					t.Fatalf("attention = %+v, want none", att)
				}
				return
			}
			if !ok || att.State != observe.AttentionNeedsInput || att.Reason == nil || *att.Reason != *tc.want {
				t.Fatalf("attention = %+v, %v; want needs_input with reason %+v", att, ok, *tc.want)
			}
		})
	}
}

func TestAgentHookNotificationKeepsTheReason(t *testing.T) {
	s := newHookServer(t)
	postHook(t, s, "tok-a", `{"hook_event_name":"Elicitation","session_id":"sid-a","mcp_server_name":"github"}`)

	postHook(t, s, "tok-a", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog","session_id":"sid-a"}`)

	att, _ := s.attention.Get("leo-a")
	if att.State != observe.AttentionNeedsInput || att.Reason == nil || att.Reason.Kind != observe.AttentionReasonElicitation {
		t.Fatalf("attention = %+v, want needs_input keeping the elicitation reason", att)
	}
}

func TestAgentHookToolSettledClearsNeedsInput(t *testing.T) {
	for _, event := range []string{"PostToolUse", "PostToolUseFailure", "PermissionDenied"} {
		t.Run(event, func(t *testing.T) {
			s := newHookServer(t)
			postHook(t, s, "tok-a", `{"hook_event_name":"PermissionRequest","session_id":"sid-a","tool_name":"Bash","tool_input":{"command":"ls"}}`)

			postHook(t, s, "tok-a", `{"hook_event_name":"`+event+`","session_id":"sid-a"}`)

			att, _ := s.attention.Get("leo-a")
			if att.State != observe.AttentionWorking || att.Reason != nil {
				t.Fatalf("attention = %+v, want working without a reason", att)
			}
		})
	}
}

func TestAgentHookSubagentToolDoesNotClearMainLoopPrompt(t *testing.T) {
	s := newHookServer(t)
	postHook(t, s, "tok-a", `{"hook_event_name":"PermissionRequest","session_id":"sid-a","tool_name":"Bash","tool_input":{"command":"ls"}}`)

	postHook(t, s, "tok-a", `{"hook_event_name":"PostToolUse","session_id":"sid-a","agent_id":"sub-1"}`)

	if att, _ := s.attention.Get("leo-a"); att.State != observe.AttentionNeedsInput {
		t.Fatalf("attention = %+v, want the main loop's prompt still pending", att)
	}
}

// hookFeedServer is a hook server whose leo-a is bridged under key "k" and
// whose bridge feed is real.
func hookFeedServer(t *testing.T) (*Server, *observe.BridgeFeed) {
	t.Helper()
	s := newHookServer(t)
	feed := observe.NewBridgeFeed(
		observe.KeyResolverFunc(func(key string) (string, bool) { return "leo-a", key == "k" }),
		nil, observe.WithFeedAttention(s.attention))
	s.attention.BindBridgeKey("k", "launch-1", "leo-a")
	s.bridgeFeed = feed
	return s, feed
}

func TestAgentHookCountsSubagents(t *testing.T) {
	s, feed := hookFeedServer(t)
	hook := func(event, id string) {
		postHook(t, s, "tok-a", `{"hook_event_name":"`+event+`","session_id":"sid-a","agent_id":"`+id+`","agent_type":"general-purpose"}`)
	}

	hook("SubagentStart", "a")
	hook("SubagentStart", "a")
	hook("SubagentStart", "b")
	if got := feed.BridgeAgents()["leo-a"].Subagents; got != 2 {
		t.Fatalf("subagents = %d, want 2", got)
	}

	hook("SubagentStop", "a")
	if got := feed.BridgeAgents()["leo-a"].Subagents; got != 1 {
		t.Fatalf("subagents after a stop = %d, want 1", got)
	}

	s.attention.BindBridgeKey("k", "launch-2", "leo-a") // a relaunch
	hook("SubagentStart", "c")
	if got := feed.BridgeAgents()["leo-a"].Subagents; got != 1 {
		t.Fatalf("subagents on the new launch = %d, want 1", got)
	}
}

func TestAgentHookReportsCompaction(t *testing.T) {
	s, _ := hookFeedServer(t)
	pub := &recordingFeedPublisher{}
	feed := observe.NewBridgeFeed(
		observe.KeyResolverFunc(func(key string) (string, bool) { return "leo-a", key == "k" }),
		pub, observe.WithFeedAttention(s.attention))
	s.bridgeFeed = feed

	postHook(t, s, "tok-a", `{"hook_event_name":"PreCompact","session_id":"sid-a","trigger":"auto"}`)
	postHook(t, s, "tok-a", `{"hook_event_name":"PostCompact","session_id":"sid-a","trigger":"auto","compact_summary":"x"}`)

	want := []observe.AgentCompactionPayload{
		{Agent: "leo-a", Phase: observe.CompactionStarted, Trigger: observe.CompactionAuto},
		{Agent: "leo-a", Phase: observe.CompactionCompleted, Trigger: observe.CompactionAuto},
	}
	if len(pub.compactions) != len(want) {
		t.Fatalf("compactions = %+v, want %+v", pub.compactions, want)
	}
	for i, got := range pub.compactions {
		if got.Agent != want[i].Agent || got.Phase != want[i].Phase || got.Trigger != want[i].Trigger {
			t.Fatalf("compaction %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// recordingFeedPublisher keeps the compaction events published to it.
type recordingFeedPublisher struct{ compactions []observe.AgentCompactionPayload }

func (p *recordingFeedPublisher) Publish(ev observe.Event) {
	if c, ok := ev.Payload.(*observe.AgentCompactionPayload); ok {
		p.compactions = append(p.compactions, *c)
	}
}
