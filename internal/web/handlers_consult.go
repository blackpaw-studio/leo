package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// handleAPIConsult runs a one-off consultant and returns its answer directly.
//
// POST /api/consult {"from":"...", "template":"...", "model":"...", "prompt":"..."}
func (s *Server) handleAPIConsult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From     string `json:"from"`
		Template string `json:"template"`
		Model    string `json:"model,omitempty"`
		Prompt   string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.Template == "" || req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "template and prompt are required"})
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}

	// A supervised caller contributes its workspace. Other callers are valid
	// too; an empty workspace makes the consultant inherit the daemon cwd.
	workspace := ""
	if req.From != "" && s.agentSvc != nil {
		if rec, err := s.agentSvc.Resolve(req.From); err == nil {
			workspace = rec.Workspace
		}
	}

	result, err := s.consults.Consult(r.Context(), cfg, consult.Request{
		Template: req.Template, Model: req.Model, Prompt: req.Prompt, Workspace: workspace,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: result})
}
