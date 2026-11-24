package web

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/devices"
)

// TestWebMuxHasNoCredentialRoutes: the plugin-credential routes act as the
// operator with another account's token, so they live on the daemon's
// Unix-socket mux only. No token the web mux accepts may reach them.
func TestWebMuxHasNoCredentialRoutes(t *testing.T) {
	s := newTokenSplitServer(t)
	store := devices.NewStore(filepath.Join(t.TempDir(), "state"), nil)
	s.devices = store
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	_, deviceToken, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}

	routes := []struct{ method, path string }{
		{"GET", "/credentials/accounts"},
		{"POST", "/credentials/check"},
		{"POST", "/credentials/fetch"},
		{"GET", "/api/credentials/accounts"},
		{"POST", "/api/credentials/fetch"},
		{"GET", "/api/v1/credentials/accounts"},
		{"POST", "/api/v1/credentials/check"},
		{"POST", "/api/v1/credentials/fetch"},
	}
	for _, tok := range []struct{ name, token string }{
		{"operator", testAPIToken}, {"agent", testAgentToken}, {"device", deviceToken},
	} {
		for _, r := range routes {
			body := ""
			if r.method == "POST" {
				body = `{"kind":"claude-code","dir":"/x"}`
			}
			w := requestAs(t, s, r.method, r.path, tok.token, body)
			// The operator token is accepted everywhere on this mux, so a 404
			// proves no handler is registered. The agent and device tokens
			// may be turned away earlier (401/403 on the UI catch-all); what
			// matters is that nobody gets a routed answer.
			want := map[int]bool{http.StatusNotFound: true}
			if tok.name != "operator" {
				want[http.StatusUnauthorized], want[http.StatusForbidden] = true, true
			}
			if !want[w.Code] {
				t.Errorf("%s token: %s %s = %d, want not routed (%s)", tok.name, r.method, r.path, w.Code, w.Body)
			}
		}
	}
}
