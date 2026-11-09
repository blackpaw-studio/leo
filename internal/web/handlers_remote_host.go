package web

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// requireAgentOrOperatorToken refuses, with 403, any caller that does not hold
// the agent token or the operator's api.token. Unlike requireOperatorToken it
// does not accept a paired device's token (the /api/* mux admits those, and a
// route that returns a peer's credential must not) nor a trusted proxy's SSO
// identity.
func (s *Server) requireAgentOrOperatorToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !matchesAnyToken(extractBearer(r.Header.Get("Authorization")), []string{s.apiToken, s.agentToken}) {
			writeJSON(w, http.StatusForbidden, apiResponse{Error: "agent or operator token required"})
			return
		}
		h(w, r)
	}
}

// remoteHostInfo is what an agent's MCP process needs to message a peer.
type remoteHostInfo struct {
	APIURL    string `json:"api_url"`
	APIToken  string `json:"api_token"`
	APIClient string `json:"api_client"`
}

// handleAPIRemoteHost resolves client.hosts.<host> for cross-host messaging.
// GET /api/remote-host/{host}
//
// The MCP server does not load leo.yaml, so the daemon, which does and runs
// as the same user, reads the config fresh (edits apply without a restart)
// and resolves the token's secret reference. The response carries a
// credential: nothing here logs it, and errors name the reference, never the
// value.
func (s *Server) handleAPIRemoteHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("host")
	cfg, ok := s.loadConfigForAPI(w)
	if !ok {
		return
	}
	host, found := cfg.Client.Hosts[name]
	switch {
	case !found:
		writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("unknown host %q (not in client.hosts)", name)})
		return
	case host.APIURL == "" || host.APIToken == nil:
		writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("host %q has no api_url/api_token configured", name)})
		return
	}
	client, err := s.remoteClientName(name, host)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	expander := config.Expander{Secrets: s.secretResolver}
	token, err := expander.ResolveValue("client.hosts."+name, "api_token", *host.APIToken)
	if err != nil {
		// #nosec G706 -- name matched a client.hosts key above, and %q quotes
		// it; err is a *config.SecretResolutionError naming the reference,
		// never the value.
		log.Printf("web: resolving api_token for host %q: %v", name, err)
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: fmt.Sprintf("resolving api_token for host %q: %v", name, err)})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: remoteHostInfo{APIURL: host.APIURL, APIToken: token, APIClient: client}})
}

// remoteClientName is the identity this machine presents to host's daemon:
// the host's api_client, else the lowercased short hostname.
func (s *Server) remoteClientName(name string, host config.HostConfig) (string, error) {
	if host.APIClient != "" {
		return host.APIClient, nil
	}
	hostname := s.hostname
	if hostname == nil {
		hostname = os.Hostname
	}
	full, err := hostname()
	short, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(full)), ".")
	if err != nil || short == "" || !ValidClientName(short) {
		return "", fmt.Errorf("cannot determine this host's name for client.hosts.%s: set client.hosts.%s.api_client", name, name)
	}
	return short, nil
}
