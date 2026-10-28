package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/agentmove"
)

// MoveJobs starts agent moves and copies in the background (agentmove.Jobs).
type MoveJobs interface {
	Start(ctx context.Context, req agentmove.Request) (agentmove.JobStatus, error)
	Get(id string) (agentmove.JobStatus, bool)
}

// WithMoveJobs wires POST /api/agent/{name}/move and GET /api/agent/moves/{id}.
func WithMoveJobs(j MoveJobs) Option {
	return func(s *Server) { s.moveJobs = j }
}

// maxMoveRequestBytes bounds a move request body.
const maxMoveRequestBytes = 64 << 10

// handleAPIAgentMove starts a move or copy of the path's agent and answers
// 202 with the job at once: a move of the caller outlives its MCP server.
// POST /api/agent/{name}/move  {mode: "move"|"copy", to, force, name, branch}
func (s *Server) handleAPIAgentMove(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil || s.moveJobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent moves not available"})
		return
	}
	var req agentmove.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, maxMoveRequestBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	rec, status, err := resolveAgentQuery(s.agentSvc, r.PathValue("name"))
	if err != nil {
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	req.Agent = rec.Name
	st, err := s.moveJobs.Start(r.Context(), req)
	if err != nil {
		code := agentmove.CodeOf(err)
		status := http.StatusConflict
		if code == agentmove.CodeBadRequest {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, apiResponse{Error: err.Error(), Code: code})
		return
	}
	writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: st})
}

// handleAPIAgentMoveStatus answers a move or copy job's current status, the
// poll for the id the POST returned. A job the registry no longer holds
// (never existed, or pruned after finishing) is a 404.
// GET /api/agent/moves/{id} (mounted as /api/agent/{name}/{id}; see web.go)
func (s *Server) handleAPIAgentMoveStatus(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") != "moves" {
		http.NotFound(w, r)
		return
	}
	if s.moveJobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent moves not available"})
		return
	}
	st, ok := s.moveJobs.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: "no such move job"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: st})
}
