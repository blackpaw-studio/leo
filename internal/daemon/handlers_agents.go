package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// resolveAgentOrError resolves a shorthand query against the agent manager and
// writes the appropriate HTTP error (404 not found, 409 ambiguous, 500 other)
// when resolution fails. Returns the canonical Record and true on success.
func (s *Server) resolveAgentOrError(w http.ResponseWriter, query string) (agent.Record, bool) {
	rec, err := s.agentMgr.Resolve(query)
	if err == nil {
		return rec, true
	}
	var nf *agent.ErrNotFound
	var amb *agent.ErrAmbiguous
	switch {
	case errors.As(err, &nf):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &amb):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return agent.Record{}, false
}

// handleAgentSpawn drives agent.Manager.Spawn via POST /agents/spawn.
func (s *Server) handleAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}

	var req AgentSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	if req.Template == "" || req.Repo == "" {
		writeError(w, http.StatusBadRequest, "template and repo are required")
		return
	}

	rec, err := s.agentMgr.Spawn(agent.SpawnSpec{
		Template: req.Template,
		Repo:     req.Repo,
		Name:     req.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	data, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling record: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentList returns every running ephemeral agent.
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeJSON(w, http.StatusOK, Response{OK: true, Data: json.RawMessage("[]")})
		return
	}
	records := s.agentMgr.List()
	data, err := json.Marshal(records)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling records: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentStop stops an agent by name or shorthand (repo, repo-short,
// suffix). The server resolves the query to a canonical agent before stopping.
func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	if err := s.agentMgr.Stop(rec.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentLogs returns the most recent `lines` lines of the agent's tmux
// pane. The `name` path segment may be a shorthand query; it is resolved to
// the canonical agent before capturing the pane. Defaults to 200 lines when
// the query param is missing.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	lines := 200
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid lines param: %v", err))
			return
		}
		lines = n
	}

	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	output, err := s.agentMgr.Logs(rec.Name, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := json.Marshal(AgentLogsResponse{Output: output})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling logs: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentSession returns the tmux session name for an agent. The `name`
// path segment may be a shorthand query; the server resolves it to the
// canonical name and echoes both back to the client.
func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}

	data, err := json.Marshal(AgentSessionResponse{
		Session: s.agentMgr.SessionName(rec.Name),
		Name:    rec.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling session: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentResolve is a read-only lookup that maps a shorthand query to the
// canonical agent name and tmux session. Useful for remote clients that want
// to confirm an agent exists before taking an action.
func (s *Server) handleAgentResolve(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	data, err := json.Marshal(AgentResolveResponse{
		Name:    rec.Name,
		Session: s.agentMgr.SessionName(rec.Name),
		Repo:    rec.Repo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling resolve: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
