package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginFlow(t *testing.T) {
	s, _ := newRawTestServer(t)

	// GET /login returns the form.
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = testHost
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /login status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `name="token"`) {
		t.Fatal("login form missing token input")
	}

	// POST /login with wrong token -> 401.
	form := url.Values{"token": {"nope"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("bad token status = %d, want 401", w.Code)
	}

	// POST /login with correct token -> 303 + leo_session cookie.
	form = url.Values{"token": {testAPIToken}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("good token status = %d, want 303", w.Code)
	}
	cookies := w.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == "leo_session" {
			sess = c
			break
		}
	}
	if sess == nil || sess.Value == "" {
		t.Fatalf("missing leo_session cookie: %+v", cookies)
	}
	if !sess.HttpOnly {
		t.Error("cookie not HttpOnly")
	}
	if sess.SameSite != http.SameSiteStrictMode {
		t.Error("cookie SameSite != Strict")
	}

	// GET / with cookie -> 200.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testHost
	req.AddCookie(&http.Cookie{Name: "leo_session", Value: sess.Value})
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET / with cookie status = %d", w.Code)
	}

	// GET / without cookie and without Bearer -> 303 /login.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testHost
	req.Header.Set("Accept", "text/html")
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("GET / no auth status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("redirect Location = %q, want /login", loc)
	}

	// POST /logout clears cookie.
	req = httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Host = testHost
	req.AddCookie(&http.Cookie{Name: "leo_session", Value: sess.Value})
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", w.Code)
	}
	var cleared *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "leo_session" {
			cleared = c
			break
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("expected cookie clear, got %+v", cleared)
	}
}

func TestSessionMiddleware_BearerStillWorks(t *testing.T) {
	s, _ := newRawTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("bearer-only GET / status = %d", w.Code)
	}
}

func TestSessionMiddleware_NonHTMLReturns401(t *testing.T) {
	s, _ := newRawTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/web/task/foo/toggle", nil)
	req.Host = testHost
	// No Accept: text/html, no cookie, no Bearer.
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauth POST status = %d, want 401", w.Code)
	}
}

func TestStaticAndLoginAreUnauthenticated(t *testing.T) {
	s, _ := newRawTestServer(t)
	for _, path := range []string{"/login", "/static/style.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = testHost
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("GET %s status = %d, want 200", path, w.Code)
		}
	}
}

func TestSafeRedirect(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/dashboard", "/dashboard"},
		{"/partials/tasks", "/partials/tasks"},
		{"//evil.com", "/"},
		{"//evil.com/path", "/"},
		{`/\evil.com`, "/"},
		{`/\\evil.com`, "/"},
		{"evil.com", "/"},
		{"http://evil.com", "/"},
		{`\evil.com`, "/"},
	}
	for _, tc := range cases {
		if got := safeRedirect(tc.in); got != tc.want {
			t.Errorf("safeRedirect(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
