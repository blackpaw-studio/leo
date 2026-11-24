package web

import (
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
)

// Cross-device read state and the open-asks list that back the apps'
// Needs-you inbox (docs/configuration/chat.md). The seen routes live under
// /api/v1/agents like the other per-agent app routes, on the TCP listener
// with the operator or a paired device's token.

// registerSeenRoutes registers the agent-seen routes on mux, each wrapped by
// wrap. PUT /agents/seen and PUT /agents/{name}/seen differ in segment
// count, so they never overlap.
func (s *Server) registerSeenRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("PUT /api/v1/agents/seen", wrap(s.handleAgentSeenBatch))
	mux.HandleFunc("GET /api/v1/agents/seen", wrap(s.handleAgentSeenList))
	mux.HandleFunc("PUT /api/v1/agents/{name}/seen", wrap(s.handleAgentSeen))
}

// seenView is one agent's stamp as the routes return it.
type seenView struct {
	Agent  string    `json:"agent"`
	SeenAt time.Time `json:"seen_at"`
}

// PUT /api/v1/agents/{name}/seen — the agent was just looked at.
func (s *Server) handleAgentSeen(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	name := r.PathValue("name")
	held, err := s.chat.MarkSeen([]string{name})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: seenView{Agent: name, SeenAt: held[name]}})
}

// PUT /api/v1/agents/seen {"agents":[...]} — several agents were looked at
// (Clear Finished). Answers each agent's stamp.
func (s *Server) handleAgentSeenBatch(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	var req struct {
		Agents []string `json:"agents"`
	}
	if !decodeChatJSON(w, r, &req) {
		return
	}
	held, err := s.chat.MarkSeen(req.Agents)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: held})
}

// GET /api/v1/agents/seen — every agent's stamp, {agent: RFC 3339}.
func (s *Server) handleAgentSeenList(w http.ResponseWriter, _ *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	seen, err := s.chat.Seen()
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: seen})
}

// GET /api/v1/chat/asks?state=open — every open ask and permission message
// on this host as {messages}, oldest first, each in the messages route's JSON shape. Only
// state=open is served: it is the one set an inbox needs, and answering
// others would mean paging.
func (s *Server) handleChatOpenAsks(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	if r.URL.Query().Get("state") != "open" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "state=open is required"})
		return
	}
	asks, err := s.chat.OpenAsks()
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string][]chat.Message{"messages": asks}})
}
