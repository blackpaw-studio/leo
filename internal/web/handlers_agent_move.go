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
}

// WithMoveJobs wires POST /api/agent/{name}/move.
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
