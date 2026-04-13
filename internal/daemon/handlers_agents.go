package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/agent"
)

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

// handleAgentStop stops an agent by name.
func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	if err := s.agentMgr.Stop(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentLogs returns the most recent `lines` lines of the agent's tmux pane.
// Defaults to 200 lines when the query param is missing.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
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

	output, err := s.agentMgr.Logs(name, lines)
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

// handleAgentSession returns the tmux session name for an agent. Clients use
// this before `tmux attach` to confirm the agent exists and learn its session.
func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	// Confirm the agent is running — SessionName alone is pure string formatting.
	found := false
	for _, rec := range s.agentMgr.List() {
		if rec.Name == name {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("agent %q not running", name))
		return
	}

	data, err := json.Marshal(AgentSessionResponse{Session: s.agentMgr.SessionName(name)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling session: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
