package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testClientToken belongs to an external, unsupervised agent (an opencode
// container). Unlike the agent token it is scoped: one route, one target.
const testClientToken = "test-client-token"

// newClientTokenServer builds a server that knows all three token kinds.
func newClientTokenServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0750); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	// A process provider is required: unlike the agent-token tests (which send
	// form data and bounce at the JSON decode) these requests carry a valid
	// body and reach the message handler itself.
	procs := &mockProcesses{states: map[string]ProcessStateInfo{}}
	return New(cfgPath, procs, nil, nil, &mockAgentService{}, Options{
		Port:       testPort,
		APIToken:   testAPIToken,
		AgentToken: testAgentToken,
		Clients: []ClientPolicy{{
			Name:       "docker-scout",
			Token:      testClientToken,
			CanMessage: []string{"leo-coding-leo", "scout-*"},
		}},
	})
}

func postAsClient(t *testing.T, s *Server, path, from string) int {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": "hello", "from": from})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return requestAs(t, s, "POST", path, testClientToken, string(body)).Code
}

// TestClientTokenReachesItsAllowedTarget: the one thing the token is for.
func TestClientTokenReachesItsAllowedTarget(t *testing.T) {
	s := newClientTokenServer(t)

	for _, target := range []string{"leo-coding-leo", "scout-alpha"} {
		code := postAsClient(t, s, "/web/agent/"+target+"/message", "docker-scout#ses_abc")
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("POST to allowed target %q = %d; the client must reach its own target", target, code)
		}
	}
}

// TestClientTokenCannotMessageOtherAgents is the boundary: a token that leaks
// out of an unsupervised container must not reach the rest of the fleet.
func TestClientTokenCannotMessageOtherAgents(t *testing.T) {
	s := newClientTokenServer(t)

	for _, target := range []string{"rocket", "olympus", "leo-coding-le", "scout"} {
		if code := postAsClient(t, s, "/web/agent/"+target+"/message", "docker-scout"); code != http.StatusForbidden {
			t.Errorf("POST to disallowed target %q = %d, want 403", target, code)
		}
	}
}

// TestClientTokenCannotReachAPIRoutes: no spawning, stopping, or task running.
func TestClientTokenCannotReachAPIRoutes(t *testing.T) {
	s := newClientTokenServer(t)

	for _, path := range []string{
		"/api/agent/spawn", "/api/agent/stop", "/api/agent/list",
		"/api/task/deploy/run", "/api/consult", "/api/v1/state",
	} {
		w := requestAs(t, s, "POST", path, testClientToken, "{}")
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s with client token = %d, want 403", path, w.Code)
		}
	}
}

// TestClientTokenCannotReachOtherAgentVerbs: messaging only — no interrupting
// or driving another agent's session.
func TestClientTokenCannotReachOtherAgentVerbs(t *testing.T) {
	s := newClientTokenServer(t)

	for _, path := range []string{
		"/web/agent/leo-coding-leo/interrupt",
		"/web/agent/leo-coding-leo/send",
		"/web/agent/leo-coding-leo/stop",
	} {
		if code := postAsClient(t, s, path, "docker-scout"); code != http.StatusForbidden {
			t.Errorf("POST %s with client token = %d, want 403", path, code)
		}
	}
}

// TestClientTokenCannotBrowseOrLogIn: the config editor renders env values in
// full, and a scoped token must never be exchangeable for a session.
func TestClientTokenCannotBrowseOrLogIn(t *testing.T) {
	s := newClientTokenServer(t)

	for _, path := range []string{"/", "/config/settings", "/agents"} {
		w := requestAs(t, s, "GET", path, testClientToken, "")
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s with client token = %d, want 403", path, w.Code)
		}
		if strings.Contains(w.Body.String(), testSecretEnvValue) {
			t.Errorf("GET %s with client token leaked a template env value", path)
		}
	}

	form := url.Values{"token": {testClientToken}}.Encode()
	w := requestAs(t, s, "POST", "/login", "", form)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /login with client token = %d, want 401", w.Code)
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), sessionCookieName) {
		t.Error("client token minted a session cookie")
	}
}

// TestClientTokenFromMustMatchTheClient: `from` carries the reply address, so
// one client must not be able to impersonate another and steal replies.
func TestClientTokenFromMustMatchTheClient(t *testing.T) {
	s := newClientTokenServer(t)
	path := "/web/agent/leo-coding-leo/message"

	for _, from := range []string{"rocket", "docker-scout-evil", "other#ses_x", "", "docker-scout#"} {
		if code := postAsClient(t, s, path, from); code != http.StatusBadRequest {
			t.Errorf("POST with from=%q = %d, want 400", from, code)
		}
	}
	for _, from := range []string{"docker-scout", "docker-scout#ses_abc123"} {
		if code := postAsClient(t, s, path, from); code == http.StatusBadRequest {
			t.Errorf("POST with valid from=%q = 400", from)
		}
	}
}

// TestExistingTokensUnaffected: adding scoped clients must not change what the
// operator and agent tokens can do.
func TestExistingTokensUnaffected(t *testing.T) {
	s := newClientTokenServer(t)

	if w := requestAs(t, s, "GET", "/api/agent/list", testAgentToken, ""); w.Code != http.StatusOK {
		t.Errorf("agent token GET /api/agent/list = %d, want 200", w.Code)
	}
	if w := requestAs(t, s, "GET", "/api/agent/list", testAPIToken, ""); w.Code != http.StatusOK {
		t.Errorf("api token GET /api/agent/list = %d, want 200", w.Code)
	}
	if w := requestAs(t, s, "GET", "/config/settings", testAPIToken, ""); w.Code != http.StatusOK {
		t.Errorf("api token GET /config/settings = %d, want 200", w.Code)
	}
	// The agent token keeps its unscoped messaging reach.
	w := requestAs(t, s, "POST", "/web/agent/some-other-agent/message", testAgentToken, `{"text":"hi"}`)
	if w.Code == http.StatusForbidden {
		t.Error("agent token got 403 on a message route; existing behavior changed")
	}
}
