package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testAgentToken is the token handed to spawned agents as LEO_API_TOKEN. It is
// deliberately NOT the operator's login token.
const testAgentToken = "test-agent-token"

// newTokenSplitServer builds a server that knows both tokens, with the
// Host/Origin check satisfied but no auth pre-applied — these tests exercise
// the auth layer itself.
func newTokenSplitServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0750); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	svc := &mockAgentService{}
	return New(cfgPath, nil, nil, nil, svc, Options{
		Port:       testPort,
		APIToken:   testAPIToken,
		AgentToken: testAgentToken,
	})
}

func requestAs(t *testing.T, s *Server, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Host = testHost
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// TestAgentTokenReachesAPIRoutes: agents must keep working. The MCP server
// calls /api/* with LEO_API_TOKEN.
func TestAgentTokenReachesAPIRoutes(t *testing.T) {
	s := newTokenSplitServer(t)

	for _, path := range []string{"/api/agent/list", "/api/template/list"} {
		if w := requestAs(t, s, "GET", path, testAgentToken, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s with agent token = %d, want 200", path, w.Code)
		}
	}
}

// TestAgentTokenReachesMessageRoutes: leo_send_message, leo_interrupt and the
// key-send tool post to /web/agent/{name}/… , which lives on the browser mux.
// Those specific routes stay agent-callable.
func TestAgentTokenReachesMessageRoutes(t *testing.T) {
	s := newTokenSplitServer(t)

	// The interrupt handler kicks off a background delayed-Escape burst.
	// Disable it and wait for the completion seam so no goroutine outlives
	// this test and races the shared interrupt-timing vars.
	oldAttempts := interruptDelayedAttempts
	interruptDelayedAttempts = 0
	defer func() { interruptDelayedAttempts = oldAttempts }()
	done := make(chan struct{})
	s.afterInterruptBurst = func() { close(done) }

	for _, path := range []string{
		"/web/agent/leo-coding-leo/message",
		"/web/agent/leo-coding-leo/send",
		"/web/agent/leo-coding-leo/interrupt",
	} {
		w := requestAs(t, s, "POST", path, testAgentToken, "text=hi")
		if w.Code == http.StatusUnauthorized {
			t.Errorf("POST %s with agent token = 401; agent messaging must keep working", path)
		}
	}
	<-done
}

// TestAgentTokenCannotLogIn is the point of the split: a token that leaks out
// of an agent — into a transcript, a log, a channel message — must not be
// exchangeable for a full web UI session.
func TestAgentTokenCannotLogIn(t *testing.T) {
	s := newTokenSplitServer(t)

	form := url.Values{"token": {testAgentToken}}.Encode()
	w := requestAs(t, s, "POST", "/login", "", form)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /login with agent token = %d, want 401", w.Code)
	}
	if cookie := w.Header().Get("Set-Cookie"); strings.Contains(cookie, sessionCookieName) {
		t.Errorf("agent token minted a session cookie: %q", cookie)
	}
}

// TestOperatorTokenStillLogsIn guards against locking the human out.
func TestOperatorTokenStillLogsIn(t *testing.T) {
	s := newTokenSplitServer(t)

	form := url.Values{"token": {testAPIToken}}.Encode()
	w := requestAs(t, s, "POST", "/login", "", form)

	if w.Code != http.StatusSeeOther {
		t.Errorf("POST /login with operator token = %d, want 303", w.Code)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), sessionCookieName) {
		t.Error("operator login did not set a session cookie")
	}
}

// TestAgentTokenCannotReadConfigPages: the config editor renders template env
// values in full, so it must stay behind the operator's credentials.
func TestAgentTokenCannotReadConfigPages(t *testing.T) {
	s := newTokenSplitServer(t)

	for _, path := range []string{"/config/templates", "/config/templates/coding", "/config/settings", "/"} {
		w := requestAs(t, s, "GET", path, testAgentToken, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with agent token = %d, want 401", path, w.Code)
		}
		if strings.Contains(w.Body.String(), testSecretEnvValue) {
			t.Errorf("GET %s with agent token leaked a template env value", path)
		}
	}
}

// TestOperatorTokenStillReachesBrowserRoutes: the bearer path on browser
// routes is what scripts and channel plugins use; it must survive the split.
func TestOperatorTokenStillReachesBrowserRoutes(t *testing.T) {
	s := newTokenSplitServer(t)

	if w := requestAs(t, s, "GET", "/config/templates", testAPIToken, ""); w.Code != http.StatusOK {
		t.Errorf("GET /config/templates with operator token = %d, want 200", w.Code)
	}
}

// TestEnsureAgentTokenIsDistinct: the two tokens must not be the same value,
// or the split is cosmetic.
func TestEnsureAgentTokenIsDistinct(t *testing.T) {
	stateDir := t.TempDir()

	apiToken, err := EnsureAPIToken(stateDir)
	if err != nil {
		t.Fatalf("EnsureAPIToken: %v", err)
	}
	agentToken, err := EnsureAgentToken(stateDir)
	if err != nil {
		t.Fatalf("EnsureAgentToken: %v", err)
	}

	if agentToken == apiToken {
		t.Error("agent token equals the operator token; the split gains nothing")
	}
	if agentToken == "" {
		t.Fatal("agent token is empty")
	}

	// Stable across calls, and stored 0600 like the operator token.
	again, err := EnsureAgentToken(stateDir)
	if err != nil {
		t.Fatalf("EnsureAgentToken (second call): %v", err)
	}
	if again != agentToken {
		t.Errorf("agent token changed between calls: %q then %q", agentToken, again)
	}
	info, err := os.Stat(AgentTokenPath(stateDir))
	if err != nil {
		t.Fatalf("stat agent token: %v", err)
	}
	if perm := info.Mode().Perm(); perm != apiTokenFileMode {
		t.Errorf("agent token file perm = %o, want %o", perm, apiTokenFileMode)
	}
}

// TestAgentCallableBrowserPath pins the allowlist that decides which browser
// routes the agent token may reach. This is the trickiest logic in the split:
// too loose and an agent reaches the config editor, too tight and agent
// messaging breaks.
func TestAgentCallableBrowserPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// The three routes the in-agent MCP server drives.
		{"/web/agent/leo-coding-leo/message", true},
		{"/web/agent/leo-coding-leo/send", true},
		{"/web/agent/leo-coding-leo/interrupt", true},

		// Everything else on the browser mux stays operator-only.
		{"/web/agent/leo-coding-leo/stop", false},
		{"/web/agent/leo-coding-leo/rename", false},
		{"/web/agent/spawn", false},
		{"/web/agents/restart", false},
		{"/config/templates/coding", false},
		{"/", false},

		// Shapes that try to smuggle an allowed suffix past the check.
		{"/web/agent/x/message/", false},
		{"/web/agent/x/y/message", false},
		{"/web/agent/x/message/../../../config/templates", false},
		{"/web/agent//message", true}, // empty name: one separator, routes to a 404 handler, not a leak
		{"/web/agent/message", false},
		{"/webs/agent/x/message", false},
		{"/web/agent/x/MESSAGE", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := agentCallableBrowserPath(tt.path); got != tt.want {
			t.Errorf("agentCallableBrowserPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
