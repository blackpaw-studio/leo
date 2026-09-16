package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// handleAPIDispatch starts a headless subagent without tying its lifetime to
// the request. Unlike consult, callers must state the target workspace.
func (s *Server) handleAPIDispatch(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	var req struct {
		From, Template, Model, Prompt, Cwd, Name string
		Mode                                     consult.Mode `json:"mode"`
		TimeoutSeconds                           *float64     `json:"timeout_seconds"`
		Notify                                   *bool        `json:"notify"`
		Isolation                                string       `json:"isolation"`
		CallerPaneID                             string       `json:"caller_pane_id"`
	}
	if err := decodeDispatchJSON(r, &req); err != nil {
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
	timeout, err := dispatchTimeout(req.TimeoutSeconds, r.URL.Query().Get("timeout_seconds"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = consult.ModeHeadless
	}
	if mode != consult.ModeHeadless && mode != consult.ModeInteractive {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "mode must be headless or interactive"})
		return
	}
	caller, err := s.resolveDispatchCaller(req.From, req.CallerPaneID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	started, err := s.consults.Start(r.Context(), cfg, consult.Request{Caller: req.From, Template: req.Template, Model: req.Model, Prompt: req.Prompt, Cwd: req.Cwd, Name: req.Name, Timeout: timeout, Mode: mode, Notify: req.Notify, Isolation: req.Isolation, CallerPaneID: caller.PaneID, CallerSessionID: caller.SessionID, CallerWindowID: caller.WindowID, CallerHarness: caller.Harness})
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

func (s *Server) handleAPIDispatchSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	cfg, cfgErr := s.loadConfig()
	if cfgErr != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", cfgErr)})
		return
	}
	result, err := s.consults.SendWithConfig(r.Context(), cfg, r.PathValue("id"), req.Message)
	if err != nil {
		// Send failures are ordinary state conflicts: callers need both the
		// reason and current state without treating the daemon as unavailable.
		status := "unknown"
		if rec, getErr := s.consults.Get(r.PathValue("id")); getErr == nil {
			status = string(rec.Status)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(struct {
			Error  string `json:"error"`
			Status string `json:"status"`
		}{err.Error(), status})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: result})
}

func (s *Server) handleAPIDispatchReport(w http.ResponseWriter, r *http.Request) {
	var report consult.HookReport
	if err := decodeDispatchJSON(r, &report); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	// Reports intentionally acknowledge unknown, duplicate, and terminal runs
	// so hook retries stop. Dispatcher.Report only returns transport errors.
	if err := s.consults.Report(r.PathValue("id"), report); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]bool{"accepted": true}})
}

func decodeDispatchJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func dispatchTimeout(jsonSeconds *float64, querySeconds string) (time.Duration, error) {
	seconds := jsonSeconds
	if querySeconds != "" {
		parsed, err := strconv.ParseFloat(querySeconds, 64)
		if err != nil {
			return 0, fmt.Errorf("timeout_seconds must be a non-negative number of seconds")
		}
		seconds = &parsed
	}
	if seconds == nil {
		return 0, nil
	}
	if *seconds < 0 || math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds > float64(time.Duration(1<<63-1))/float64(time.Second) {
		return 0, fmt.Errorf("timeout_seconds must be a non-negative number of seconds")
	}
	return time.Duration(*seconds * float64(time.Second)), nil
}

func (s *Server) handleAPIDispatchGet(w http.ResponseWriter, r *http.Request) {
	record, err := s.consults.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}
	record = s.consults.Collect(record)
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

func (s *Server) handleAPIDispatchRelease(w http.ResponseWriter, r *http.Request) {
	record, err := s.consults.Release(r.PathValue("id"))
	if err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "unknown dispatch") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, apiResponse{Error: err.Error()})
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
