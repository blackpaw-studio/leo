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
