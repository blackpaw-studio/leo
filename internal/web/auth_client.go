package web

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// ClientPolicy is one external API client: an agent Leo does not supervise
// (typically a container) that holds a bearer token of its own.
//
// This is deliberately not leotools.Permissions. There, an empty allowlist
// means "unrestricted" — the right default for a template Leo itself spawned.
// A client token lives outside Leo's trust boundary, so here an empty
// CanMessage means "nothing", and every route not named below is denied.
type ClientPolicy struct {
	Name       string
	Token      string
	CanMessage []string
}

// allowsTarget reports whether this client may message target. Matching is on
// the literal path segment, exact or as a glob, mirroring template
// permissions — except that an empty list denies everything. A malformed
// pattern never matches: config validation is where a bad pattern should be
// caught, and a parse failure here must not widen access.
func (c ClientPolicy) allowsTarget(target string) bool {
	for _, entry := range c.CanMessage {
		if entry == target {
			return true
		}
		if ok, err := path.Match(entry, target); err == nil && ok {
			return true
		}
	}
	return false
}

// allowsFrom reports whether from is an identity this client may claim. It is
// either the client's own name or "<name>#<suffix>" — the suffix carries the
// caller's session so a reply can be addressed back to it. Anything else would
// let one client impersonate another and collect its replies, which is the
// whole reason `from` is checked rather than trusted.
func (c ClientPolicy) allowsFrom(from string) bool {
	if from == c.Name {
		return true
	}
	prefix := c.Name + "#"
	if !strings.HasPrefix(from, prefix) {
		return false
	}
	return validSessionSuffix(strings.TrimPrefix(from, prefix))
}

// maxSessionSuffix bounds the session id a client may claim. Real ids are ~30
// characters; the cap and charset keep an unbounded, arbitrary-byte string out
// of the identity that Leo records and shows to the receiving agent.
const maxSessionSuffix = 128

// validSessionSuffix reports whether s is a plausible session identifier:
// non-empty, bounded, and drawn from an alphabet with no whitespace or
// punctuation that could be mistaken for structure in the rendered prefix.
func validSessionSuffix(s string) bool {
	if s == "" || len(s) > maxSessionSuffix {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// lookupClient returns the policy owning token. It compares against every
// configured client without an early return, so the work done does not depend
// on which client matched — same discipline as matchesAnyToken.
func (s *Server) lookupClient(token string) (ClientPolicy, bool) {
	if token == "" {
		return ClientPolicy{}, false
	}
	var found ClientPolicy
	ok := false
	for _, c := range s.clients {
		if subtle.ConstantTimeCompare([]byte(token), []byte(c.Token)) == 1 {
			found, ok = c, true
		}
	}
	return found, ok
}

// sanitizeClientText neutralizes the two things an untrusted body could do to
// the line Leo types into the target's pane: a newline would submit the turn
// early (splitting one message into several), and an embedded prefix would
// forge a second sender for an agent parsing the real one.
func sanitizeClientText(text string) string {
	replaced := strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	return strings.ReplaceAll(replaced, "[message from", "[message-from")
}

// clientMessageTarget returns the agent named by a /web/agent/<name>/message
// path, and whether the path has exactly that shape. Nested paths that merely
// end in /message are rejected.
func clientMessageTarget(urlPath string) (string, bool) {
	const prefix = "/web/agent/"
	const suffix = "/message"
	if !strings.HasPrefix(urlPath, prefix) || !strings.HasSuffix(urlPath, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(urlPath, prefix), suffix)
	// Dot segments are refused outright rather than left to the mux, which
	// would answer a cleaned path with a redirect.
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return "", false
	}
	return name, true
}

// serveClient handles a request authenticated by a client token. It is
// default-deny: only POST /web/agent/<allowed>/message with a `from` the
// client is entitled to claim reaches the application, and everything else
// — every /api/* route, every other agent verb, the browser UI — is refused
// here rather than falling through to the middleware the operator and agent
// tokens use.
func (s *Server) serveClient(w http.ResponseWriter, r *http.Request, policy ClientPolicy, next http.Handler) {
	target, ok := clientMessageTarget(r.URL.Path)
	if !ok || r.Method != http.MethodPost {
		writeJSON(w, http.StatusForbidden, apiResponse{
			Error: fmt.Sprintf("client %q may only POST to /web/agent/<target>/message", policy.Name),
		})
		return
	}
	if !policy.allowsTarget(target) {
		writeJSON(w, http.StatusForbidden, apiResponse{
			Error: fmt.Sprintf("client %q is not permitted to message %q", policy.Name, target),
		})
		return
	}

	// The body is read to validate `from` and then replayed for the handler.
	// bodySizeMiddleware has already capped it, so this is bounded.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "could not read request body"})
		return
	}
	var payload struct {
		Text string `json:"text"`
		From string `json:"from"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}
	if !policy.allowsFrom(payload.From) {
		writeJSON(w, http.StatusBadRequest, apiResponse{
			Error: fmt.Sprintf("from must be %q or %q followed by a session id", policy.Name, policy.Name+"#"),
		})
		return
	}

	// Rewrite the body rather than forwarding it. Leo types `text` verbatim
	// into the target's pane and never renders `from` there, so validating
	// `from` alone would leave the client free to write its own
	// "[message from someone-else]" line into the text and impersonate
	// another sender to the receiving agent. The authenticated identity is
	// stamped on here, and only the two fields the handler reads survive.
	rewritten, err := json.Marshal(map[string]string{
		"text": fmt.Sprintf(leotools.MessagePrefixFormat, payload.From, sanitizeClientText(payload.Text)),
		"from": payload.From,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "could not re-encode message"})
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	next.ServeHTTP(w, r)
}

// validClients drops entries that cannot be honored safely: an unnamed or
// tokenless client, and — critically — any client whose token equals the
// operator or agent token. Those two are resolved after clients in the
// dispatcher, so an overlapping value would silently downgrade them to a
// one-route scope and lock the operator out of the UI.
func validClients(clients []ClientPolicy, apiToken, agentToken string) []ClientPolicy {
	out := make([]ClientPolicy, 0, len(clients))
	seen := make(map[string]string, len(clients))
	for _, c := range clients {
		switch {
		case !ValidClientName(c.Name) || c.Token == "":
			fmt.Fprintf(os.Stderr, "warning: web: ignoring API client %q: invalid name or empty token\n", c.Name)
		case c.Token == apiToken || c.Token == agentToken:
			fmt.Fprintf(os.Stderr, "warning: web: ignoring API client %q: its token collides with the api or agent token\n", c.Name)
		case seen[c.Token] != "":
			fmt.Fprintf(os.Stderr, "warning: web: ignoring API client %q: shares a token with %q\n", c.Name, seen[c.Token])
		default:
			seen[c.Token] = c.Name
			out = append(out, c)
		}
	}
	return out
}

// ValidClientName reports whether name is safe to use as a client identity.
//
// Config.Validate checks this too, but the daemon builds the web server from
// config.Load without validating (internal/service/process.go), so a
// hand-edited or corrupted leo.yaml would otherwise reach this code. The name
// becomes a filename under state/clients and the identity `from` is checked
// against, so a separator would escape the directory and a "#" would collapse
// the boundary between one client's identity and another's.
func ValidClientName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 64 {
		return false
	}
	if strings.ContainsAny(name, `/\#`) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
