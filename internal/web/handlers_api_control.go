package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// Control API limits (docs/specs/2026-10-06-bridge-observe.md, "Control
// endpoints"). A body cap sits above each field cap so JSON escaping of a
// legal field still fits, while a body far past it is cut off unread.
const (
	maxControlMessageBytes      = 256 << 10
	maxControlMessageBody       = 4 * maxControlMessageBytes
	maxCompactInstructionsBytes = 4 << 10
	maxCompactBody              = 4 * maxCompactInstructionsBytes
)

// controlRoutes are the agent control operations, keyed by verb. The same
// handlers serve /api/v1/agents/{name}/<verb> (operator token) and the
// daemon socket's /agents/{name}/<verb> (see ControlHandler).
func (s *Server) controlRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"message":   s.handleAPIControlMessage,
		"interrupt": s.handleAPIControlInterrupt,
		"compact":   s.handleAPIControlCompact,
		"clear":     s.handleAPIControlClear,
	}
}

// registerControlRoutes registers every control verb on mux under
// <prefix>/{name}/<verb>, each wrapped by wrap.
func (s *Server) registerControlRoutes(mux *http.ServeMux, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc) {
	for verb, h := range s.controlRoutes() {
		mux.HandleFunc("POST "+prefix+"/{name}/"+verb, wrap(h))
	}
}

// ControlHandler serves the agent control operations unauthenticated at
// POST /agents/{name}/{message,interrupt,compact,clear}, for the daemon to
// mount on its unix socket, whose 0600 mode is the auth.
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerControlRoutes(mux, "/agents", func(h http.HandlerFunc) http.HandlerFunc { return h })
	return mux
}

// isOperator reports whether r carries the operator's own credential — the
// operator bearer token or an SSO identity from a trusted proxy — as
// opposed to an agent token, which also passes /api/* bearer auth.
func (s *Server) isOperator(r *http.Request) bool {
	return matchesAnyToken(extractBearer(r.Header.Get("Authorization")), []string{s.apiToken}) || proxyAuthenticated(r, s.trustedProxies)
}

// requireOperator refuses any caller but the operator with 403.
func (s *Server) requireOperator(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOperator(r) {
			writeJSON(w, http.StatusForbidden, apiResponse{Error: "operator token required"})
			return
		}
		h(w, r)
	}
}

// writeAPIControl renders a control outcome in the /api/v1 shape: the
// transport on success, {queued: true} on any 202.
func writeAPIControl(w http.ResponseWriter, out controlOutcome) {
	switch {
	case out.err != "":
		writeJSON(w, out.status, apiResponse{Error: out.err})
	case out.status == http.StatusAccepted:
		writeJSON(w, out.status, apiResponse{OK: true, Data: map[string]bool{"queued": true}})
	default:
		writeJSON(w, out.status, apiResponse{OK: true, Data: map[string]string{"transport": out.transport}})
	}
}

// decodeControlBody decodes r's JSON body, capped at limit, into v. An
// empty body is allowed when optional (v keeps its zero value). On failure
// it writes the response — 413 past the cap, 400 otherwise — and reports
// false.
func decodeControlBody(w http.ResponseWriter, r *http.Request, limit int64, optional bool, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.Is(err, io.EOF) && optional:
		return true
	case errors.As(err, &tooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: fmt.Sprintf("request body exceeds %d bytes", limit)})
	default:
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
	}
	return false
}

// requireLiveAgent answers 404 and reports false when name is not a
// running agent. message does its own check, since it may wake a dormant
// agent.
func (s *Server) requireLiveAgent(w http.ResponseWriter, name string) bool {
	if _, ok := s.processStates()[name]; ok {
		return true
	}
	writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("no such agent %q", name)})
	return false
}

// handleAPIControlMessage delivers a message into an agent's prompt.
// POST /api/v1/agents/{name}/message  {"text": "...", "from": "..."}
func (s *Server) handleAPIControlMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		// From is display only, never authorization (see
		// handleWebAgentMessage).
		From string `json:"from"`
	}
	if !decodeControlBody(w, r, maxControlMessageBody, false, &req) {
		return
	}
	switch {
	case req.Text == "":
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "text is required"})
		return
	case len(req.Text) > maxControlMessageBytes:
		writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: fmt.Sprintf("text exceeds %d bytes", maxControlMessageBytes)})
		return
	}
	writeAPIControl(w, s.messageAgent(r.Context(), r.PathValue("name"), req.From, req.Text))
}

// handleAPIControlInterrupt interrupts an agent's running turn.
// POST /api/v1/agents/{name}/interrupt
func (s *Server) handleAPIControlInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.requireLiveAgent(w, name) {
		return
	}
	writeAPIControl(w, s.interruptAgent(r.Context(), name))
}

// handleAPIControlCompact compacts an agent's conversation.
// POST /api/v1/agents/{name}/compact  {"instructions": "..."} (optional)
func (s *Server) handleAPIControlCompact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instructions string `json:"instructions"`
	}
	if !decodeControlBody(w, r, maxCompactBody, true, &req) {
		return
	}
	if len(req.Instructions) > maxCompactInstructionsBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: fmt.Sprintf("instructions exceed %d bytes", maxCompactInstructionsBytes)})
		return
	}
	name := r.PathValue("name")
	if !s.requireLiveAgent(w, name) {
		return
	}
	writeAPIControl(w, s.slashCommandAgent(name, "compact", req.Instructions, bridge.Compact(req.Instructions)))
}

// handleAPIControlClear clears an agent's conversation.
// POST /api/v1/agents/{name}/clear
func (s *Server) handleAPIControlClear(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.requireLiveAgent(w, name) {
		return
	}
	writeAPIControl(w, s.slashCommandAgent(name, "clear", "", bridge.Clear()))
}
