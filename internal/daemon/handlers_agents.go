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
// when resolution fails. Ambiguous and not-found responses carry a machine-
// readable Code and (for ambiguous) the candidate Matches so clients can
// reconstruct typed errors. Returns the canonical Record and true on success.
func (s *Server) resolveAgentOrError(w http.ResponseWriter, query string) (agent.Record, bool) {
	rec, err := s.agentMgr.Resolve(query)
	if err == nil {
		return rec, true
	}
	var nf *agent.ErrNotFound
	var amb *agent.ErrAmbiguous
	switch {
	case errors.As(err, &nf):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotFound})
	case errors.As(err, &amb):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAmbiguous, Matches: amb.Matches})
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

	rec, err := s.agentMgr.Spawn(r.Context(), agent.SpawnSpec{
		Template: req.Template,
		Repo:     req.Repo,
		Name:     req.Name,
		Branch:   req.Branch,
		Base:     req.Base,
	})
	if err != nil {
		writeAgentError(w, err)
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

// handleAgentPrune removes the worktree and agentstore record for a stopped
// worktree agent. No-op (surfaced as 400) for shared-workspace agents. The
// `name` path segment must be an exact agent name because shorthand resolution
// only matches live agents and a prunable agent has already been stopped.
func (s *Server) handleAgentPrune(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	var req AgentPruneRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
			return
		}
	}

	if err := s.agentMgr.Prune(r.Context(), name, agent.PruneOptions{
		Force:        req.Force,
		DeleteBranch: req.DeleteBranch,
	}); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// writeAgentError translates agent-package typed errors into HTTP responses
// with stable machine-readable Code fields so the CLI client can reconstruct
// errors.Is matches on the other side of the socket.
func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrWorktreeRequiresSlash):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeWorktreeRequireSep})
	case errors.Is(err, agent.ErrAgentStillRunning):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentStillRunning})
	case errors.Is(err, agent.ErrNotWorktreeAgent):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotWorktreeAgent})
	case errors.Is(err, agent.ErrWorktreeDirty):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeWorktreeDirty})
	case errors.Is(err, agent.ErrBranchCheckedOut):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchCheckedOut})
	case errors.Is(err, agent.ErrBranchNotMerged):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchNotMerged})
	case errors.Is(err, agent.ErrBranchNotFound):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchNotFound})
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
