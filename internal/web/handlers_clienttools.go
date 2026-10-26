package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/clienttools"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

// Client-tools relay HTTP surface (docs/configuration/client-tools.md). The app routes
// live under /api/v1/client-tools on TCP (operator and paired-device tokens)
// and, unauthenticated, at the same paths on the daemon's 0600 socket. The
// agent routes (list, call) are TCP-only and take the agent token.

const (
	clientToolsPrefix = "/api/v1/client-tools"
	// maxClientToolsJSONBody caps the small request bodies.
	maxClientToolsJSONBody = 1 << 20
	// maxClientToolResultBody caps a result body: an app returns screenshots
	// as base64 image blocks, far past the global request cap.
	maxClientToolResultBody = leotools.MaxClientToolResultBytes
)

// isClientToolResultPath reports whether path is a result post.
func isClientToolResultPath(path string) bool {
	return strings.HasPrefix(path, clientToolsPrefix+"/requests/") && strings.HasSuffix(path, "/result")
}

// clientToolsAPI serves the relay's app routes. It is shared by the web
// server (TCP) and the daemon socket.
type clientToolsAPI struct{ relay func() *clienttools.Relay }

// register mounts the app routes on mux, each wrapped by wrap (the auth
// layer; identity for the socket).
func (a clientToolsAPI) register(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST "+clientToolsPrefix, wrap(a.handleRegister))
	mux.HandleFunc("GET "+clientToolsPrefix, wrap(a.handleList))
	mux.HandleFunc("GET "+clientToolsPrefix+"/requests", wrap(a.handlePending))
	mux.HandleFunc("POST "+clientToolsPrefix+"/requests/{id}/claim", wrap(a.handleClaim))
	mux.HandleFunc("POST "+clientToolsPrefix+"/requests/{id}/result", wrap(a.handleResult))
}

// NewClientToolsHandler serves the app routes unauthenticated, for the
// daemon to mount on its unix socket, whose 0600 mode is the auth.
func NewClientToolsHandler(relay func() *clienttools.Relay) http.Handler {
	mux := http.NewServeMux()
	clientToolsAPI{relay: relay}.register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	return mux
}

// registerClientToolsRoutes mounts the app routes (operator only) and the
// agent routes on the TCP API mux.
func (s *Server) registerClientToolsRoutes(apiMux *http.ServeMux) {
	api := clientToolsAPI{relay: func() *clienttools.Relay { return s.clientTools }}
	api.register(apiMux, s.requireOperatorToken)
	apiMux.HandleFunc("GET /api/agent/{name}/client-tools", api.handleList)
	apiMux.HandleFunc("POST /api/agent/{name}/client-tools/call", api.handleCall)
}

// providerFor returns the provider adapter for a `?client_tools=1` events
// stream, or nil for any other stream.
func (s *Server) providerFor(r *http.Request) (provider clienttools.ProviderStream, ok bool) {
	if r.URL.Query().Get("client_tools") != "1" || s.clientTools == nil {
		return clienttools.ProviderStream{}, false
	}
	return s.clientTools.Provider(), true
}

func (a clientToolsAPI) available(w http.ResponseWriter) *clienttools.Relay {
	relay := a.relay()
	if relay == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "client tools are not available on this daemon", Code: "unavailable"})
	}
	return relay
}

func decodeClientToolsJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: "request body too large", Code: "too_large"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body: " + err.Error(), Code: "invalid_request"})
		return false
	}
	return true
}

func writeClientToolsError(w http.ResponseWriter, status int, err error, code string) {
	writeJSON(w, status, apiResponse{Error: err.Error(), Code: code})
}

// POST /api/v1/client-tools
func (a clientToolsAPI) handleRegister(w http.ResponseWriter, r *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	var req struct {
		ClientID string             `json:"client_id"`
		Group    string             `json:"group"`
		Tools    []clienttools.Tool `json:"tools"`
	}
	if !decodeClientToolsJSON(w, r, maxClientToolsJSONBody, &req) {
		return
	}
	rev, err := relay.Register(clienttools.ClientID(req.ClientID), req.Group, req.Tools)
	if err != nil {
		var re *clienttools.RegisterError
		if !errors.As(err, &re) {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		resp := apiResponse{Error: re.Message, Code: re.Code}
		status := http.StatusBadRequest
		switch re.Code {
		case clienttools.CodeUnknownClient:
			status = http.StatusNotFound
		case clienttools.CodeNameConflict:
			status = http.StatusConflict
			resp.Data = map[string]string{"tool": re.Tool}
		}
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]uint64{"revision": rev}})
}

// GET /api/v1/client-tools and GET /api/agent/{name}/client-tools
func (a clientToolsAPI) handleList(w http.ResponseWriter, _ *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	list := relay.List()
	if list.Tools == nil {
		list.Tools = []clienttools.ListedTool{}
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: list})
}

// GET /api/v1/client-tools/requests?pending=1
func (a clientToolsAPI) handlePending(w http.ResponseWriter, _ *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	pending := relay.Pending()
	if pending == nil {
		pending = []clienttools.PendingRequest{}
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]any{"requests": pending}})
}

// POST /api/v1/client-tools/requests/{id}/claim
func (a clientToolsAPI) handleClaim(w http.ResponseWriter, r *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	var req struct {
		ClientID string `json:"client_id"`
	}
	if !decodeClientToolsJSON(w, r, maxClientToolsJSONBody, &req) {
		return
	}
	claimed, err := relay.Claim(r.PathValue("id"), clienttools.ClientID(req.ClientID))
	if err != nil {
		writeClientToolsRelayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]any{"request": claimed}})
}

// POST /api/v1/client-tools/requests/{id}/result
func (a clientToolsAPI) handleResult(w http.ResponseWriter, r *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	var req struct {
		ClientID string                 `json:"client_id"`
		OK       bool                   `json:"ok"`
		Content  json.RawMessage        `json:"content"`
		Error    *clienttools.ToolError `json:"error"`
	}
	if !decodeClientToolsJSON(w, r, maxClientToolResultBody, &req) {
		return
	}
	err := relay.Settle(r.PathValue("id"), clienttools.ClientID(req.ClientID), clienttools.Result{OK: req.OK, Content: req.Content, Error: req.Error})
	if err != nil {
		writeClientToolsRelayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{}})
}

// POST /api/agent/{name}/client-tools/call
func (a clientToolsAPI) handleCall(w http.ResponseWriter, r *http.Request) {
	relay := a.available(w)
	if relay == nil {
		return
	}
	// The call blocks for as long as the app takes (a handoff has no
	// timeout), past the server's 30 s WriteTimeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if !decodeClientToolsJSON(w, r, maxClientToolsJSONBody, &req) {
		return
	}
	res, err := relay.Call(r.Context(), r.PathValue("name"), req.Tool, req.Args)
	if err != nil {
		writeClientToolsRelayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: res})
}

// writeClientToolsRelayError maps a relay error to its status and wire code.
func writeClientToolsRelayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clienttools.ErrUnknownTool):
		writeClientToolsError(w, http.StatusNotFound, err, "unknown_tool")
	case errors.Is(err, clienttools.ErrUnknownRequest):
		writeClientToolsError(w, http.StatusNotFound, err, "unknown_request")
	case errors.Is(err, clienttools.ErrUnknownClient):
		writeClientToolsError(w, http.StatusNotFound, err, "unknown_client")
	case errors.Is(err, clienttools.ErrAlreadyClaimed):
		writeClientToolsError(w, http.StatusConflict, err, "already_claimed")
	case errors.Is(err, clienttools.ErrOwnedByOther):
		writeClientToolsError(w, http.StatusConflict, err, "owned_by_other")
	case errors.Is(err, clienttools.ErrNotProvider):
		writeClientToolsError(w, http.StatusConflict, err, "not_provider")
	case errors.Is(err, clienttools.ErrNotClaimant):
		writeClientToolsError(w, http.StatusConflict, err, "not_claimant")
	case errors.Is(err, clienttools.ErrAlreadySettled):
		writeClientToolsError(w, http.StatusConflict, err, "already_settled")
	case errors.Is(err, clienttools.ErrInvalidResult):
		writeClientToolsError(w, http.StatusBadRequest, err, "invalid_result")
	default:
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
	}
}
