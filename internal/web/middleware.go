package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/netip"
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
// The port argument is the port the web server is actually listening on. For
// untrusted peers Host and Origin (if present) must match it exactly. Trusted
// proxy peers still require an allowed hostname, but may omit or use any port
// because reverse proxies normally forward their public Host and Origin. Empty Origin is allowed for
// tools that never set it (curl, channel plugins making server-to-server
// requests). Request method is not considered — GETs are blocked too because
// several endpoints return config JSON.
//
// extraHosts lists additional hostnames/IPs (beyond loopback) that are
// permitted — used when the web UI is bound to a LAN interface.
//
// public lists exact origins (web.public_urls) whose Host and Origin are
// accepted verbatim, without the listener-port requirement: a tunnel or
// reverse proxy serves them on its own port.
func hostOriginMiddleware(port int, extraHosts []string, trusted []netip.Prefix, next http.Handler, public ...publicOrigin) http.Handler {
	publicHosts := make(map[string]struct{}, len(public))
	publicOriginSet := make(map[string]struct{}, len(public))
	for _, p := range public {
		publicHosts[p.host] = struct{}{}
		publicOriginSet[p.origin] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(loopbackHosts)+len(extraHosts))
	for k := range loopbackHosts {
		allowed[k] = struct{}{}
	}
	for _, h := range extraHosts {
		allowed[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, parsed := peerAddr(r)
		requirePort := !parsed || !isTrustedPeer(peer, trusted)
		if _, ok := publicHosts[strings.ToLower(r.Host)]; !ok {
			if err := checkHost(r.Host, port, allowed, requirePort); err != nil {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if _, ok := publicOriginSet[strings.ToLower(origin)]; ok {
				next.ServeHTTP(w, r)
				return
			}
			if err := checkOrigin(origin, port, allowed, requirePort); err != nil {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// publicOrigin is one web.public_urls entry reduced to the exact Host header
// ("host" or "host:port", lowercased) and Origin ("scheme://host[:port]")
// it is reached by.
type publicOrigin struct {
	host   string
	origin string
}

// publicOrigins parses web.public_urls, skipping entries that do not parse
// (config validation rejects them before they get here).
func publicOrigins(urls []string) []publicOrigin {
	out := make([]publicOrigin, 0, len(urls))
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := strings.ToLower(u.Host)
		out = append(out, publicOrigin{host: host, origin: strings.ToLower(u.Scheme) + "://" + host})
	}
	return out
}

// checkHost parses an HTTP Host header ("host:port" or bracketed "[ipv6]:port")
// and verifies the hostname is in the allowed set. When requirePort is true
// (an untrusted peer), its port must match the listener port. When false (a
// trusted proxy peer), a missing or any port is accepted because it names the
// proxy's public listener.
//
// Hostname comparison is case-insensitive per RFC 3986 §3.2.2, so
// http://LOCALHOST:8370 is treated the same as http://localhost:8370.
func checkHost(host string, port int, allowed map[string]struct{}, requirePort bool) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		if !requirePort {
			h = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
			p = ""
		} else {
			// Host may not include a port (e.g. "localhost"); reject, since we
			// always serve on a non-default port.
			return fmt.Errorf("bad host %q: %w", host, err)
		}
	}
	if _, ok := allowed[strings.ToLower(h)]; !ok {
		return fmt.Errorf("host %q not allowed", h)
	}
	if requirePort && p != fmt.Sprintf("%d", port) {
		return fmt.Errorf("host port %q != listener port %d", p, port)
	}
	return nil
}

// checkOrigin validates an Origin header. It must parse as http:// or https://
// with a hostname in the allowed set. When requirePort is true (an untrusted
// peer), its port must match the listener port. When false (a trusted proxy
// peer), a missing or any port is accepted because it names the proxy's public
// listener.
func checkOrigin(origin string, port int, allowed map[string]struct{}, requirePort bool) error {
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
	if requirePort && p == "" {
		// Origin without explicit port implies default (80/443), which never
		// matches our listener.
		return fmt.Errorf("origin %q missing port", origin)
	}
	if requirePort && p != fmt.Sprintf("%d", port) {
		return fmt.Errorf("origin port %q != listener port %d", p, port)
	}
	return nil
}

// agentCallableBrowserSuffixes are the /web/agent/{name}/… routes the in-agent
// MCP server drives: leo_send_message posts /message, leo_interrupt posts
// /interrupt, leo_compact and leo_clear post /compact and /clear, and key
// sends post /send. They live on the browser mux for the UI's benefit, but
// agents legitimately need them.
//
// Everything else on that mux — the config editor above all, which renders
// template env values in full — stays operator-only, so an agent cannot turn
// its own token into a full UI session.
var agentCallableBrowserSuffixes = []string{"/message", "/send", "/interrupt", "/compact", "/clear"}

// agentCallableBrowserPath reports whether a browser-mux path is one the agent
// token may reach.
func agentCallableBrowserPath(path string) bool {
	if !strings.HasPrefix(path, "/web/agent/") {
		return false
	}
	for _, suffix := range agentCallableBrowserSuffixes {
		if strings.HasSuffix(path, suffix) {
			// Reject a nested path pretending to be one of ours: the route
			// shape is exactly /web/agent/<name>/<verb>.
			rest := strings.TrimPrefix(path, "/web/agent/")
			if strings.Count(rest, "/") == 1 {
				return true
			}
		}
	}
	return false
}

// matchesAnyToken reports whether got equals any of the accepted tokens,
// comparing in constant time. Empty candidates never match.
func matchesAnyToken(got string, accepted []string) bool {
	if got == "" {
		return false
	}
	ok := false
	for _, tok := range accepted {
		if tok == "" {
			continue
		}
		// No early return: compare against every candidate so the work done
		// does not depend on which token matched.
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1 {
			ok = true
		}
	}
	return ok
}

// bearerAuthMiddleware gates requests on an Authorization: Bearer <token>
// header, accepting any of the supplied tokens. At least one must be
// non-empty; callers are expected to short-circuit rather than install the
// middleware with nothing configured. Comparison uses constant-time equality.
//
// Multiple tokens exist because agents get their own, less privileged one —
// see EnsureAgentToken.
func bearerAuthMiddleware(tokens []string, trusted []netip.Prefix, next http.Handler) http.Handler {
	return bearerAuthWith(tokens, nil, trusted, next)
}

// bearerAuthWith is bearerAuthMiddleware that also accepts any bearer for
// which extra (if non-nil) reports true — paired device tokens, which live in
// a store rather than a fixed list.
func bearerAuthWith(tokens []string, extra func(string) bool, trusted []netip.Prefix, next http.Handler) http.Handler {
	configured := false
	for _, tok := range tokens {
		if tok != "" {
			configured = true
		}
	}
	if !configured {
		// Safety valve: rather than silently disable auth, refuse to serve.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "api token not configured", http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proxyAuthenticated(r, trusted) {
			logProxyAuthentication(r)
			next.ServeHTTP(w, r)
			return
		}
		got := extractBearer(r.Header.Get("Authorization"))
		if got == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="leo"`)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if !acceptsBearer(got, tokens, extra) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="leo"`)
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// acceptsBearer reports whether got matches one of tokens or, when extra is
// non-nil, satisfies it. Both checks always run so timing does not reveal
// which kind of credential matched.
func acceptsBearer(got string, tokens []string, extra func(string) bool) bool {
	ok := matchesAnyToken(got, tokens)
	if extra != nil && got != "" && extra(got) {
		ok = true
	}
	return ok
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

// bodyLimit raises the body cap for one exact POST path (the chat upload).
type bodyLimit struct {
	// path matches one exact path; match, when set, matches any path instead.
	path  string
	match func(path string) bool
	max   int64
}

func (o bodyLimit) applies(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if o.match != nil {
		return o.match(r.URL.Path)
	}
	return r.URL.Path == o.path
}

// bodySizeMiddleware enforces max on every request body, or an override's
// own cap for a POST to its exact path. A read past the limit returns a 413
// to the handler via http.MaxBytesReader.
func bodySizeMiddleware(max int64, next http.Handler, overrides ...bodyLimit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			limit := max
			for _, o := range overrides {
				if o.applies(r) {
					limit = o.max
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
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
