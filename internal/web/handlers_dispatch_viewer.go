package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) viewerOperatorOnly(r *http.Request) bool {
	return matchesAnyToken(extractBearer(r.Header.Get("Authorization")), []string{s.apiToken}) || proxyAuthenticated(r, s.trustedProxies)
}

func (s *Server) handleDispatchViewerCloseFinished(w http.ResponseWriter, r *http.Request) {
	if !s.viewerOperatorOnly(r) {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "operator token required"})
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeDispatchJSON(r, &req); err != nil || req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "session_id is required"})
		return
	}
	records, err := s.consults.CloseFinished(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: records})
}

type saveViewerDefaultsRequest struct {
	Placement *string `json:"placement,omitempty"`
	MaxPanes  *int    `json:"max_panes,omitempty"`
}

func (s *Server) handleDispatchViewerSaveDefault(w http.ResponseWriter, r *http.Request) {
	if !s.viewerOperatorOnly(r) {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "operator token required"})
		return
	}
	var req saveViewerDefaultsRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}
	viewer := cfg.Defaults.Dispatch.Viewer
	if req.Placement != nil {
		if strings.TrimSpace(*req.Placement) == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "placement must be pane or window"})
			return
		}
		viewer.Placement = *req.Placement
	}
	if req.MaxPanes != nil {
		n := *req.MaxPanes
		viewer.MaxPanes = &n
	}
	cfg.Defaults.Dispatch.Viewer = viewer
	if msg := s.validateAndSave(cfg); msg != "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: msg})
		return
	}
	warn := s.reloadConfigOrWarn()
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: struct {
		Saved    bool   `json:"saved"`
		Reloaded bool   `json:"reloaded"`
		Warning  string `json:"warning,omitempty"`
	}{true, warn == "", warn}})
}
