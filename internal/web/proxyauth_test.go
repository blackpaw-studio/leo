package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestParseTrustedProxies(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"10.0.2.9", "10.0.4.4/24"})
	if err != nil || len(trusted) != 2 {
		t.Fatalf("parseTrustedProxies = %v, %v; want two prefixes and nil", trusted, err)
	}
	if _, err := parseTrustedProxies([]string{"invalid"}); err == nil {
		t.Fatal("parseTrustedProxies accepted invalid address")
	}
	trusted, err = parseTrustedProxies([]string{"::ffff:10.0.2.0/120"})
	if err != nil {
		t.Fatalf("parseTrustedProxies mapped prefix: %v", err)
	}
	if got, want := trusted[0].String(), "10.0.2.0/24"; got != want {
		t.Fatalf("mapped prefix = %q, want %q", got, want)
	}
	for _, value := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "fe80::1%en0"} {
		if _, err := parseTrustedProxies([]string{value}); err == nil {
			t.Errorf("parseTrustedProxies accepted %q", value)
		}
	}
}

func TestSessionMiddleware_TrustedProxyAuthentication(t *testing.T) {
	testProxyAuthentication(t, func(trusted []string) http.Handler {
		return sessionMiddleware(newSessionStore(time.Hour), []string{"token"}, mustTrustedProxies(t, trusted), okHandler())
	}, true)
}

func TestBearerAuthMiddleware_TrustedProxyAuthentication(t *testing.T) {
	testProxyAuthentication(t, func(trusted []string) http.Handler {
		return bearerAuthMiddleware([]string{"token"}, mustTrustedProxies(t, trusted), okHandler())
	}, false)
}

func testProxyAuthentication(t *testing.T, middleware func([]string) http.Handler, session bool) {
	t.Helper()
	cases := []struct {
		name, remoteAddr, remoteUser, bearer string
		trusted                              []string
		want                                 int
	}{
		{"trusted header", "10.0.2.9:51234", "evan", "", []string{"10.0.2.9"}, http.StatusOK},
		{"untrusted header", "10.0.2.50:51234", "evan", "", []string{"10.0.2.9"}, http.StatusUnauthorized},
		{"trusted no header", "10.0.2.9:51234", "", "", []string{"10.0.2.9"}, http.StatusUnauthorized},
		{"untrusted bearer", "10.0.2.50:51234", "", "token", []string{"10.0.2.9"}, http.StatusOK},
		{"trusted bearer", "10.0.2.9:51234", "", "token", []string{"10.0.2.9"}, http.StatusOK},
		{"empty list", "10.0.2.9:51234", "evan", "", nil, http.StatusUnauthorized},
		{"cidr", "10.0.4.5:1", "evan", "", []string{"10.0.4.0/24"}, http.StatusOK},
		{"ipv6", "[::1]:1", "evan", "", []string{"::1"}, http.StatusOK},
		{"mapped ipv4", "[::ffff:10.0.2.9]:1", "evan", "", []string{"10.0.2.9"}, http.StatusOK},
		{"whitespace header", "10.0.2.9:51234", " \t ", "", []string{"10.0.2.9"}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertProxyAuthentication(t, middleware(tc.trusted), tc.remoteAddr, tc.remoteUser, tc.bearer, tc.want)
		})
	}
	if session {
		store := newSessionStore(time.Hour)
		id, err := store.create()
		if err != nil {
			t.Fatal(err)
		}
		h := sessionMiddleware(store, []string{"token"}, mustTrustedProxies(t, []string{"10.0.2.9"}), okHandler())
		r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
		r.RemoteAddr = "10.0.2.50:1"
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != "ok" {
			t.Fatalf("valid session from untrusted peer = %d %q, want 200 and handler body", w.Code, w.Body.String())
		}

		redirectReq := httptest.NewRequest(http.MethodGet, "http://example.test/path", nil)
		redirectReq.RemoteAddr = "10.0.2.9:1"
		redirectReq.Header.Set("Accept", "text/html")
		redirectW := httptest.NewRecorder()
		middleware([]string{"10.0.2.9"}).ServeHTTP(redirectW, redirectReq)
		if redirectW.Code != http.StatusSeeOther || redirectW.Header().Get("Location") != "/login?redirect=%2Fpath" {
			t.Fatalf("HTML failure = %d Location %q", redirectW.Code, redirectW.Header().Get("Location"))
		}
	}
}

func assertProxyAuthentication(t *testing.T, handler http.Handler, remoteAddr, remoteUser, bearer string, want int) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	r.RemoteAddr = remoteAddr
	r.Header.Set("Accept", "application/json")
	if remoteUser != "" {
		r.Header.Set("Remote-User", remoteUser)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("status = %d, want %d", w.Code, want)
	}
	if want == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != `Bearer realm="leo"` {
		t.Fatalf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
	if want == http.StatusOK && w.Body.String() != "ok" {
		t.Fatalf("successful request did not reach handler: body = %q", w.Body.String())
	}
}

func mustTrustedProxies(t *testing.T, values []string) []netip.Prefix {
	t.Helper()
	trusted, err := parseTrustedProxies(values)
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
}
