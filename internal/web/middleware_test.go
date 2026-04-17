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

// --- Host/Origin middleware tests ---

func TestHostOriginMiddleware_RejectsForeignHost(t *testing.T) {
	s, _ := newRawTestServer(t)

	form := url.Values{}
	form.Set("model", "sonnet")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "evil.example:8370"

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestHostOriginMiddleware_RejectsForeignOrigin(t *testing.T) {
	s, _ := newRawTestServer(t)

	form := url.Values{}
	form.Set("model", "sonnet")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = testHost
	req.Header.Set("Origin", "https://evil.example")

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestHostOriginMiddleware_RejectsWrongOriginPort(t *testing.T) {
	s, _ := newRawTestServer(t)

	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = testHost
	req.Header.Set("Origin", "http://127.0.0.1:9999")

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHostOriginMiddleware_AllowsEmptyOriginWithLocalHost(t *testing.T) {
	s, _ := newRawTestServer(t)

	form := url.Values{}
	form.Set("model", "sonnet")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = testHost
	// No Origin header.

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	// We don't care about the handler's status here — only that the request
	// was NOT blocked by middleware.
	if w.Code == http.StatusForbidden {
		t.Fatalf("middleware unexpectedly returned 403 for local Host + no Origin; body: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("middleware unexpectedly returned 401 for /web route; body: %s", w.Body.String())
	}
}

func TestHostOriginMiddleware_AllowsLocalhostVariants(t *testing.T) {
	s, _ := newRawTestServer(t)

	for _, h := range []string{"127.0.0.1:8370", "localhost:8370", "[::1]:8370"} {
		t.Run(h, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
			req.Host = h
			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, req)
			if w.Code == http.StatusForbidden {
				t.Fatalf("host %q unexpectedly forbidden; body: %s", h, w.Body.String())
			}
		})
	}
}

func TestHostOriginMiddleware_RejectsMissingPort(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	req.Host = "127.0.0.1" // missing :port
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing port, got %d", w.Code)
	}
}

// TestHostOriginMiddlewareRejectsBypasses locks in middleware behavior against
// common header-smuggling / bypass attempts. Each row describes an attacker-
// supplied Host or Origin value that must be rejected with 403, and exists to
// keep future refactors from accidentally loosening the check.
func TestHostOriginMiddlewareRejectsBypasses(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
	}{
		// --- Host-header bypass attempts ---
		{
			name: "suffix-confusion with localhost prefix",
			host: "localhost.attacker.com:8370",
		},
		{
			name: "double port",
			host: "127.0.0.1:8370:8370",
		},
		{
			name: "suffix-confusion with IP prefix",
			host: "127.0.0.1.attacker.com",
		},
		// --- Origin-header bypass attempts ---
		{
			name:   "userinfo smuggling",
			origin: "http://127.0.0.1:8370@attacker.com",
		},
		{
			name:   "fragment smuggling",
			origin: "http://attacker.com#@127.0.0.1:8370",
		},
		{
			name:   "literal null",
			origin: "null",
		},
		{
			name:   "file scheme",
			origin: "file:///etc/passwd",
		},
		{
			name:   "javascript scheme",
			origin: "javascript:alert(1)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRawTestServer(t)

			req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
			// Always supply a locally-valid Host so that any rejection
			// must come from the specific header under test.
			req.Host = testHost
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for host=%q origin=%q, got %d; body: %s",
					tc.host, tc.origin, w.Code, w.Body.String())
			}
		})
	}
}

// TestHostOriginMiddleware_AcceptsMixedCase confirms that RFC 3986 §3.2.2
// case-insensitive hostname matching is honored — LOCALHOST etc. should not
// be a 403.
func TestHostOriginMiddleware_AcceptsMixedCase(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
	}{
		{"uppercase host", "LOCALHOST:8370", ""},
		{"mixed case host", "LocalHost:8370", ""},
		{"uppercase origin", testHost, "http://LOCALHOST:8370"},
		{"mixed case origin", testHost, "http://LocalHost:8370"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRawTestServer(t)

			req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, req)

			if w.Code == http.StatusForbidden {
				t.Fatalf("mixed-case host %q / origin %q unexpectedly forbidden; body: %s",
					tc.host, tc.origin, w.Body.String())
			}
		})
	}
}

// --- Bearer-auth middleware tests ---

func TestBearerAuth_RejectsMissingToken(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/spawn", strings.NewReader(`{}`))
	req.Host = testHost

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestBearerAuth_RejectsWrongToken(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/spawn", strings.NewReader(`{}`))
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer wrong-token")

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestBearerAuth_AcceptsCorrectToken(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/spawn", strings.NewReader(`{}`))
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	// With no AgentService wired, the handler short-circuits with 503. We
	// only care that middleware let the request through — any status other
	// than 401/403 is fine for this assertion.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("middleware rejected a valid token (code %d); body: %s", w.Code, w.Body.String())
	}
}

func TestBearerAuth_HostCheckStillApplies(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/spawn", strings.NewReader(`{}`))
	req.Host = "evil.example:8370"
	req.Header.Set("Authorization", "Bearer "+testAPIToken)

	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign Host even with valid token, got %d", w.Code)
	}
}

// --- Unit tests for helpers ---

func TestCheckHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		port    int
		wantErr bool
	}{
		{"loopback v4", "127.0.0.1:8370", 8370, false},
		{"loopback v6", "[::1]:8370", 8370, false},
		{"localhost", "localhost:8370", 8370, false},
		{"wrong port", "127.0.0.1:9999", 8370, true},
		{"foreign host", "evil.example:8370", 8370, true},
		{"missing port", "127.0.0.1", 8370, true},
		{"empty", "", 8370, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkHost(tc.host, tc.port)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkHost(%q, %d) err = %v, wantErr %v", tc.host, tc.port, err, tc.wantErr)
			}
		})
	}
}

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		port    int
		wantErr bool
	}{
		{"http loopback", "http://127.0.0.1:8370", 8370, false},
		{"https loopback", "https://127.0.0.1:8370", 8370, false},
		{"http localhost", "http://localhost:8370", 8370, false},
		{"v6 loopback", "http://[::1]:8370", 8370, false},
		{"wrong scheme", "ftp://127.0.0.1:8370", 8370, true},
		{"foreign host", "https://evil.example:8370", 8370, true},
		{"wrong port", "http://127.0.0.1:9999", 8370, true},
		{"missing port", "http://127.0.0.1", 8370, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkOrigin(tc.origin, tc.port)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkOrigin(%q, %d) err = %v, wantErr %v", tc.origin, tc.port, err, tc.wantErr)
			}
		})
	}
}

func TestExtractBearer(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"BEARER   padded  ", "padded"},
		{"Basic abc", ""},
		{"Bearer", ""},
	}
	for _, tc := range tests {
		if got := extractBearer(tc.in); got != tc.want {
			t.Errorf("extractBearer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- Token-file tests ---

func TestEnsureAPIToken_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")

	tok, err := EnsureAPIToken(stateDir)
	if err != nil {
		t.Fatalf("EnsureAPIToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}

	info, err := os.Stat(filepath.Join(stateDir, "api.token"))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file perm = %o, want 0600", perm)
	}
}

func TestEnsureAPIToken_Idempotent(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")

	first, err := EnsureAPIToken(stateDir)
	if err != nil {
		t.Fatalf("first EnsureAPIToken: %v", err)
	}
	second, err := EnsureAPIToken(stateDir)
	if err != nil {
		t.Fatalf("second EnsureAPIToken: %v", err)
	}
	if first != second {
		t.Errorf("token changed across calls: %q != %q", first, second)
	}
}

func TestEnsureAPIToken_RejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "api.token"), []byte("   \n"), 0600); err != nil {
		t.Fatalf("write empty token: %v", err)
	}
	if _, err := EnsureAPIToken(stateDir); err == nil {
		t.Fatal("expected error for empty token file, got nil")
	}
}

func TestEnsureAPIToken_RejectsLoosePerms(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tokenPath := filepath.Join(stateDir, "api.token")
	if err := os.WriteFile(tokenPath, []byte("abcdef0123456789\n"), 0644); err != nil {
		t.Fatalf("write loose token: %v", err)
	}
	// Ensure the perm stuck despite any umask.
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatalf("chmod loose token: %v", err)
	}

	_, err := EnsureAPIToken(stateDir)
	if err == nil {
		t.Fatal("expected error for token file with 0644 perms, got nil")
	}
	if !strings.Contains(err.Error(), "644") {
		t.Errorf("error should mention actual perm %o, got: %v", 0644, err)
	}
	if !strings.Contains(err.Error(), "fix or delete") {
		t.Errorf("error should advise fix-or-delete, got: %v", err)
	}
}

func TestEnsureAPIToken_TightensLooseStateDir(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	// Pre-create the dir loose, with a correctly-permissioned token inside.
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(stateDir, 0750); err != nil {
		t.Fatalf("chmod initial state dir: %v", err)
	}
	tokenPath := filepath.Join(stateDir, "api.token")
	if err := os.WriteFile(tokenPath, []byte("abcdef0123456789\n"), 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := os.Chmod(tokenPath, 0600); err != nil {
		t.Fatalf("chmod token: %v", err)
	}

	tok, err := EnsureAPIToken(stateDir)
	if err != nil {
		t.Fatalf("EnsureAPIToken on loose dir: %v", err)
	}
	if tok != "abcdef0123456789" {
		t.Errorf("token = %q, want unchanged", tok)
	}

	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("state dir perm after EnsureAPIToken = %o, want 0700", perm)
	}
}
