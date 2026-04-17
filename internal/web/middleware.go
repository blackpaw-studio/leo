package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// allowedLocalHosts is the set of hostnames the web UI accepts in Host/Origin
// headers. We deliberately do not expose an allowlist knob: the UI has no
// built-in authn and is designed for single-user localhost access. Relaxing
// this check is equivalent to turning off auth.
var allowedLocalHosts = map[string]struct{}{
	"127.0.0.1": {},
	"::1":       {},
	"localhost": {},
}

// hostOriginMiddleware rejects requests whose Host or Origin headers are not
// local. It mitigates:
//
//   - DNS rebinding: a malicious domain resolves to 127.0.0.1 mid-flight, so
//     the browser still sends Host: attacker.example.
//   - Drive-by cross-origin POSTs: any page on the internet can POST (or GET,
//     which leaks config) to http://127.0.0.1:<port>. Browsers attach an
//     Origin header for these requests.
//
// The port argument is the port the web server is actually listening on. Host
// and Origin (if present) must match it exactly. Empty Origin is allowed for
// tools that never set it (curl, channel plugins making server-to-server
// requests). Request method is not considered — GETs are blocked too because
// several endpoints return config JSON.
func hostOriginMiddleware(port int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := checkHost(r.Host, port); err != nil {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if err := checkOrigin(origin, port); err != nil {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost parses an HTTP Host header ("host:port" or bracketed "[ipv6]:port")
// and verifies the hostname is a known loopback name and the port matches the
// listener port. A missing port is accepted only when the listener is on the
// well-known default (port 80) — in practice we always have a port.
//
// Hostname comparison is case-insensitive per RFC 3986 §3.2.2, so
// http://LOCALHOST:8370 is treated the same as http://localhost:8370.
func checkHost(host string, port int) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		// Host may not include a port (e.g. "localhost"); reject, since we
		// always serve on a non-default port.
		return fmt.Errorf("bad host %q: %w", host, err)
	}
	if _, ok := allowedLocalHosts[strings.ToLower(h)]; !ok {
		return fmt.Errorf("host %q not allowed", h)
	}
	if p != fmt.Sprintf("%d", port) {
		return fmt.Errorf("host port %q != listener port %d", p, port)
	}
	return nil
}

// checkOrigin validates an Origin header. It must parse as http:// or https://
// with a known loopback hostname and the correct port.
func checkOrigin(origin string, port int) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("bad origin %q: %w", origin, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("bad origin scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("origin %q missing host", origin)
	}
	if _, ok := allowedLocalHosts[strings.ToLower(host)]; !ok {
		return fmt.Errorf("origin host %q not allowed", host)
	}
	p := u.Port()
	if p == "" {
		// Origin without explicit port implies default (80/443), which never
		// matches our listener.
		return fmt.Errorf("origin %q missing port", origin)
	}
	if p != fmt.Sprintf("%d", port) {
		return fmt.Errorf("origin port %q != listener port %d", p, port)
	}
	return nil
}

// bearerAuthMiddleware gates requests on an Authorization: Bearer <token>
// header. token must already be non-empty; callers are expected to short-
// circuit and never install the middleware with an empty token. Comparison
// uses constant-time equality.
func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	if token == "" {
		// Safety valve: rather than silently disable auth, refuse to serve.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "api token not configured", http.StatusInternalServerError)
		})
	}
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := extractBearer(r.Header.Get("Authorization"))
		if got == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="leo"`)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="leo"`)
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractBearer returns the token from an "Authorization: Bearer xxx" header,
// or the empty string if the header is missing or the wrong shape. Scheme
// match is case-insensitive per RFC 7235.
func extractBearer(h string) string {
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
