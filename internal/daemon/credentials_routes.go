package daemon

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/user"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/credentials"
)

// Plugin-credential routes: they let the Mac app run a plugin's request as
// an account that lives on this machine, so the token never leaves it. They
// sit on the Unix-socket mux only (0600 socket, same trust as every route
// there) and are never registered on the web mux, so agent and device tokens
// cannot reach another account's token. Credential and validation failures
// are HTTP 200 with an {"error":{reason,message}} body; 4xx/5xx is for
// malformed JSON and an unreadable config only.

const (
	// maxCredentialRequestBytes bounds a fetch request: a 5 MiB body, base64
	// encoded, plus headers and the envelope.
	maxCredentialRequestBytes = 8 << 20
	// credentialIOWindow outlives the 30 s upstream timeout, so the daemon's
	// own server timeouts never cut a slow-but-allowed fetch short.
	credentialIOWindow = 45 * time.Second
)

// WithCredentials sets the fetcher and home dir behind the /credentials
// routes. Tests inject fakes; the daemon defaults to the system token source
// (Keychain on macOS) and the user's real home.
func WithCredentials(f credentials.Fetcher, home string) Option {
	return func(s *Server) {
		s.credFetcher = &f
		s.credHome = home
	}
}

// defaultCredentials fills in the system token source when no option set one.
func (s *Server) defaultCredentials() {
	if s.credFetcher != nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("daemon: credentials routes disabled: no home dir: %v", err)
		return
	}
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	WithCredentials(credentials.Fetcher{Source: credentials.NewSystemTokenSource(home, name)}, home)(s)
}

type credentialError struct {
	Error *credentials.Error `json:"error"`
}

func writeCredentialError(w http.ResponseWriter, err *credentials.Error) {
	writeJSON(w, http.StatusOK, credentialError{Error: err})
}

// credentialAccounts loads the accounts per call, so a config edit shows up
// without a restart. It writes the failure response itself.
func (s *Server) credentialAccounts(w http.ResponseWriter) ([]credentials.Account, bool) {
	if s.credFetcher == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials are not available")
		return nil, false
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		// The loader's error can quote the rejected file, so the detail
		// stays in the log.
		log.Printf("daemon: loading config for /credentials: %v", err)
		writeError(w, http.StatusInternalServerError, "config is unavailable")
		return nil, false
	}
	return credentials.Accounts(cfg, s.credHome), true
}

func (s *Server) handleCredentialAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, ok := s.credentialAccounts(w)
	if !ok {
		return
	}
	kind := credentials.Kind(r.URL.Query().Get("kind"))
	out := make([]credentials.Account, 0, len(accounts))
	for _, a := range accounts {
		if kind == "" || a.Kind == kind {
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Accounts []credentials.Account `json:"accounts"`
	}{out})
}

func (s *Server) handleCredentialCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind credentials.Kind `json:"kind"`
		Dir  string           `json:"dir"`
	}
	if !decodeCredentialJSON(w, r, &req) {
		return
	}
	accounts, ok := s.credentialAccounts(w)
	if !ok {
		return
	}
	if err := s.credFetcher.Check(r.Context(), accounts, req.Kind, req.Dir); err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

func (s *Server) handleCredentialFetch(w http.ResponseWriter, r *http.Request) {
	var req credentials.Request
	if !decodeCredentialJSON(w, r, &req) {
		return
	}
	accounts, ok := s.credentialAccounts(w)
	if !ok {
		return
	}
	resp, err := s.credFetcher.Fetch(r.Context(), accounts, req)
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// decodeCredentialJSON reads a size-capped JSON body, and widens the
// connection's deadlines past the server default for a long upstream call.
func decodeCredentialJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(credentialIOWindow)
	// Best effort: a writer without deadline support keeps the server default.
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
	r.Body = http.MaxBytesReader(w, r.Body, maxCredentialRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
