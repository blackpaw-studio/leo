package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// loopbackHosts is the set of hostnames the web UI accepts in Host/Origin
// headers by default. Additional hosts can be added via extraHosts in
// hostOriginMiddleware.
var loopbackHosts = map[string]struct{}{
	"127.0.0.1": {},
	"::1":       {},
	"localhost": {},
}

// hostOriginMiddleware rejects requests whose Host or Origin headers are not
// in the allowed set. It mitigates:
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
//
// extraHosts lists additional hostnames/IPs (beyond loopback) that are
// permitted — used when the web UI is bound to a LAN interface.
func hostOriginMiddleware(port int, extraHosts []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(loopbackHosts)+len(extraHosts))
	for k := range loopbackHosts {
		allowed[k] = struct{}{}
	}
	for _, h := range extraHosts {
		allowed[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := checkHost(r.Host, port, allowed); err != nil {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if err := checkOrigin(origin, port, allowed); err != nil {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost parses an HTTP Host header ("host:port" or bracketed "[ipv6]:port")
// and verifies the hostname is in the allowed set and the port matches the
// listener port. A missing port is accepted only when the listener is on the
// well-known default (port 80) — in practice we always have a port.
//
// Hostname comparison is case-insensitive per RFC 3986 §3.2.2, so
// http://LOCALHOST:8370 is treated the same as http://localhost:8370.
func checkHost(host string, port int, allowed map[string]struct{}) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		// Host may not include a port (e.g. "localhost"); reject, since we
		// always serve on a non-default port.
		return fmt.Errorf("bad host %q: %w", host, err)
	}
	if _, ok := allowed[strings.ToLower(h)]; !ok {
		return fmt.Errorf("host %q not allowed", h)
	}
	if p != fmt.Sprintf("%d", port) {
		return fmt.Errorf("host port %q != listener port %d", p, port)
	}
	return nil
}

// checkOrigin validates an Origin header. It must parse as http:// or https://
// with a hostname in the allowed set and the correct port.
func checkOrigin(origin string, port int, allowed map[string]struct{}) error {
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
	if _, ok := allowed[strings.ToLower(host)]; !ok {
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

// maxRequestBodyBytes caps any single request body. The web server handles
// config writes and prompt-file uploads; nothing legitimate requires more than
// a few hundred KB. 10 MiB keeps generous headroom for template edits without
// giving a misbehaving or malicious client an arbitrary disk-write handle.
const maxRequestBodyBytes int64 = 10 << 20

// bodySizeMiddleware enforces maxRequestBodyBytes on every request body.
// A read past the limit returns a 413 to the handler via http.MaxBytesReader.
func bodySizeMiddleware(max int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware sets baseline security headers on every response.
//
// The CSP permits 'unsafe-inline' for scripts and styles because the current
// htmx templates embed inline <script> blocks and onclick= attributes. Moving
// those to nonce-based inline scripts would let us drop 'unsafe-inline' from
// script-src for strong XSS containment. For now this CSP at least bounds all
// resource loading to same-origin, which prevents exfiltration to external
// hosts if an XSS is ever introduced.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
