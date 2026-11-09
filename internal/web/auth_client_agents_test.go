package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

const (
	listClientToken = "list-client-token"
	postOnlyToken   = "post-only-token"
)

func newClientAgentsServer(t *testing.T, records []agent.Record) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	return New(cfgPath, &mockProcesses{states: map[string]ProcessStateInfo{}}, nil, nil, &mockAgentService{records: records}, Options{
		Port: testPort, APIToken: testAPIToken, AgentToken: testAgentToken,
		Clients: []ClientPolicy{
			{Name: "dionysus", Token: listClientToken, CanMessage: []string{"rocket", "scout-*"}},
			{Name: "poster", Token: postOnlyToken, CanPost: []string{"rocket"}},
		},
	})
}

func TestServeClient_ListAgentsFiltered(t *testing.T) {
	s := newClientAgentsServer(t, []agent.Record{
		{Name: "rocket", Status: "running", Workspace: "/secret/ws", Template: "tmpl", Repo: "r"},
		{Name: "infra", Status: "running"},
		{Name: "scout-1", Status: "stopped"},
	})

	t.Run("only message-able agents, name and status only", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/web/client/agents", listClientToken, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		const want = `[{"name":"rocket","status":"running"},{"name":"scout-1","status":"stopped"}]`
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || string(env.Data) != want {
			t.Fatalf("data = %s (err %v), want %s", env.Data, err, want)
		}
	})
	t.Run("a client with no can_message gets the same empty 200", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/web/client/agents", postOnlyToken, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":[]`) {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "rocket") {
			t.Fatalf("leaks agent names: %s", w.Body)
		}
	})
	t.Run("POST is still refused", func(t *testing.T) {
		if w := requestAs(t, s, "POST", "/web/client/agents", listClientToken, "{}"); w.Code != http.StatusForbidden {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("other client GET paths stay refused", func(t *testing.T) {
		for _, p := range []string{"/web/client/agents/x", "/web/client", "/api/agent/list", "/web/agents"} {
			if w := requestAs(t, s, "GET", p, listClientToken, ""); w.Code != http.StatusForbidden {
				t.Errorf("GET %s = %d, want 403", p, w.Code)
			}
		}
	})
	t.Run("no token is not served", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/web/client/agents", nil)
		req.Host = testHost
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("unauthenticated request served: %s", w.Body)
		}
	})
}

func TestServeClient_ListAgentsNoRecords(t *testing.T) {
	s := newClientAgentsServer(t, nil)
	w := requestAs(t, s, "GET", "/web/client/agents", listClientToken, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

// Real agents are stored under canonical names (leo-rocket) while policies and
// docs use display names (rocket). Discovery must match either form and hand
// back a name the policy permits, so it can be passed straight back to send.
func TestServeClient_ListAgentsCanonicalAndDisplayNames(t *testing.T) {
	records := []agent.Record{
		{Name: "leo-rocket", Status: "running", Workspace: "/secret/ws"},
		{Name: "leo-scout-1", Status: "running"},
		{Name: "leo-infra", Status: "running"},
		{Name: "leo-canon-only", Status: "stopped"},
	}
	cases := []struct {
		name       string
		canMessage []string
		want       string
	}{
		{"display-name policy", []string{"rocket", "scout-*"}, `[{"name":"leo-rocket","status":"running"},{"name":"leo-scout-1","status":"running"}]`},
		{"canonical-name policy", []string{"leo-rocket"}, `[{"name":"leo-rocket","status":"running"}]`},
		{"canonical glob", []string{"leo-scout-*"}, `[{"name":"leo-scout-1","status":"running"}]`},
		{"both forms permitted: canonical wins", []string{"rocket", "leo-rocket"}, `[{"name":"leo-rocket","status":"running"}]`},
		{"nothing matches either form", []string{"nobody"}, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newClientAgentsServer(t, records)
			s.SetClients([]ClientPolicy{{Name: "dionysus", Token: listClientToken, CanMessage: tc.canMessage}})
			w := requestAs(t, s, "GET", "/web/client/agents", listClientToken, "")
			var env struct {
				Data json.RawMessage `json:"data"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &env) != nil || string(env.Data) != tc.want {
				t.Fatalf("status %d data %s, want %s", w.Code, env.Data, tc.want)
			}
		})
	}
}

// serveClientMessagePath runs a message POST through serveClient and returns
// the path the downstream mux would route (empty if the request never got
// there) and the response code.
func serveClientMessagePath(t *testing.T, s *Server, policy ClientPolicy, target string) (string, int) {
	t.Helper()
	var routed string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { routed = r.URL.Path })
	req := httptest.NewRequest("POST", "/web/agent/"+target+"/message", strings.NewReader(`{"text":"hi","from":"dionysus#scout"}`))
	req.Host = testHost
	w := httptest.NewRecorder()
	s.serveClient(w, req, policy, next)
	return routed, w.Code
}

// Discovery must hand back names that round-trip into send: an agent stored as
// leo-rocket, a policy written as `rocket`.
func TestClientMessage_DiscoveredNameRoundTripsIntoSend(t *testing.T) {
	s := newClientAgentsServer(t, []agent.Record{{Name: "leo-rocket", Status: "running"}, {Name: "leo-infra", Status: "running"}})
	policy := ClientPolicy{Name: "dionysus", Token: listClientToken, CanMessage: []string{"rocket"}}
	s.SetClients([]ClientPolicy{policy})

	w := requestAs(t, s, "GET", "/web/client/agents", listClientToken, "")
	var env struct {
		Data []clientAgent `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Data) != 1 {
		t.Fatalf("listing %s (err %v)", w.Body, err)
	}
	if env.Data[0].Name != "leo-rocket" {
		t.Fatalf("discovery returned %q, want the stored name leo-rocket", env.Data[0].Name)
	}
	routed, code := serveClientMessagePath(t, s, policy, env.Data[0].Name)
	if code != http.StatusOK || routed != "/web/agent/leo-rocket/message" {
		t.Fatalf("send to the discovered name: code %d routed %q", code, routed)
	}
}

func TestClientMessage_DisplayTargetResolvesToStoredName(t *testing.T) {
	policy := ClientPolicy{Name: "dionysus", Token: listClientToken, CanMessage: []string{"rocket"}}

	cases := []struct {
		name       string
		records    []agent.Record
		policy     ClientPolicy
		target     string
		wantRouted string
		wantCode   int
	}{
		{"display target delivers to the canonical agent", []agent.Record{{Name: "leo-rocket"}}, policy, "rocket", "/web/agent/leo-rocket/message", 200},
		{"canonical target is authorized by a display policy", []agent.Record{{Name: "leo-rocket"}}, policy, "leo-rocket", "/web/agent/leo-rocket/message", 200},
		{"a display target that exists exactly is not rewritten", []agent.Record{{Name: "rocket"}, {Name: "leo-rocket"}}, policy, "rocket", "/web/agent/rocket/message", 200},
		{"an unknown name is left for the handler's 404", nil, policy, "rocket", "/web/agent/rocket/message", 200},
		{"canonical policy authorizes the display target", []agent.Record{{Name: "leo-rocket"}}, ClientPolicy{Name: "dionysus", CanMessage: []string{"leo-rocket"}}, "rocket", "/web/agent/leo-rocket/message", 200},
		{"other agents are denied in either form", []agent.Record{{Name: "leo-infra"}}, policy, "infra", "", 403},
		{"other agents are denied canonically", []agent.Record{{Name: "leo-infra"}}, policy, "leo-infra", "", 403},
		{"a policy never reaches an unrelated exact agent via the prefix", []agent.Record{{Name: "rocket"}}, ClientPolicy{Name: "dionysus", CanMessage: []string{"leo-rocket"}}, "rocket", "", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newClientAgentsServer(t, tc.records)
			routed, code := serveClientMessagePath(t, s, tc.policy, tc.target)
			if code != tc.wantCode || routed != tc.wantRouted {
				t.Fatalf("code %d routed %q, want %d %q", code, routed, tc.wantCode, tc.wantRouted)
			}
		})
	}
}
