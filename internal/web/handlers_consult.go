package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// handleAPIDispatch starts a headless subagent without tying its lifetime to
// the request. Unlike consult, callers must state the target workspace.
func (s *Server) handleAPIDispatch(w http.ResponseWriter, r *http.Request) {
	var req struct{ From, Template, Model, Prompt, Cwd, Name string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.Cwd == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "cwd is required"})
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
	started, err := s.consults.Start(r.Context(), cfg, consult.Request{Caller: req.From, Template: req.Template, Model: req.Model, Prompt: req.Prompt, Cwd: req.Cwd, Name: req.Name})
	if err != nil {
		var validationErr *consult.ValidationError
		status := http.StatusInternalServerError
		if errors.As(err, &validationErr) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: started})
}

func (s *Server) handleAPIDispatchGet(w http.ResponseWriter, r *http.Request) {
	record, err := s.consults.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: record})
}

func (s *Server) handleAPIDispatchWait(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "at least one id is required"})
		return
	}
	timeout := consult.RunTimeout
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		seconds, err := strconv.ParseFloat(raw, 64)
		if err != nil || seconds < 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "timeout must be a non-negative number of seconds"})
			return
		}
		timeout = time.Duration(seconds * float64(time.Second))
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: s.consults.Wait(r.Context(), ids, timeout)})
}

func (s *Server) handleAPIDispatchCancel(w http.ResponseWriter, r *http.Request) {
	record, err := s.consults.Cancel(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: record})
}

// handleAPIConsult runs a one-off consultant and returns its answer directly.
//
// POST /api/consult {"from":"...", "template":"...", "model":"...", "prompt":"..."}
func (s *Server) handleAPIConsult(w http.ResponseWriter, r *http.Request) {
	// Consults legitimately outlive the server-wide 30-second WriteTimeout.
	// The consultant's own deadline (consult.RunTimeout) stays authoritative.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

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
	if workspace == "" {
		workspace, _ = os.Getwd()
	}

	result, err := s.consults.Consult(r.Context(), cfg, consult.Request{
		Template: req.Template, Model: req.Model, Prompt: req.Prompt,
		Cwd: workspace, Caller: req.From,
	})
	if err != nil {
		status := http.StatusBadGateway
		var validationErr *consult.ValidationError
		switch {
		case errors.As(err, &validationErr):
			status = http.StatusBadRequest
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(err, context.Canceled):
			status = http.StatusRequestTimeout
		}
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: result})
}
