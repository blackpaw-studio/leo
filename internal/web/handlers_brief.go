package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// maxBriefBodyBytes bounds a brief write: five bounded lines and two lists
// of five fit in a few KiB even as escaped multi-byte text.
const maxBriefBodyBytes = 32 << 10

// briefRequest is the body of POST /api/agent/brief: the launch's attention
// token plus the whole brief. It has no agent field: the agent is whoever
// the token resolves to.
type briefRequest struct {
	Token string          `json:"token"`
	Brief json.RawMessage `json:"brief"`
}

// briefResponse is the success body: {ok, brief}, brief null once cleared.
type briefResponse struct {
	OK    bool         `json:"ok"`
	Brief *brief.Brief `json:"brief"`
}

// Tri-state of the cached brief switch: unknown until first needed, then the
// value leo.yaml had at the last load or reload.
const (
	briefUnknown int32 = iota
	briefOff
	briefOn
)

// briefSwitch caches brief.enabled so the hot paths (every prompt's context
// hook, every brief write) never re-read leo.yaml.
type briefSwitch struct{ state atomic.Int32 }

// set records the switch and reports whether it differs from what was
// cached (an unknown cache counts as different).
func (b *briefSwitch) set(enabled bool) (changed bool) {
	next := briefOff
	if enabled {
		next = briefOn
	}
	return b.state.Swap(next) != next
}

// SetBriefEnabled records the machine's brief switch after a config reload.
func (s *Server) SetBriefEnabled(enabled bool) { s.briefSwitch.set(enabled) }

// briefEnabled is brief.enabled, loaded from leo.yaml once and then kept
// current by SetBriefEnabled.
func (s *Server) briefEnabled() bool {
	switch s.briefSwitch.state.Load() {
	case briefOn:
		return true
	case briefOff:
		return false
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return false
	}
	s.briefSwitch.set(cfg.Brief.Enabled)
	return cfg.Brief.Enabled
}

// handleAPIAgentBrief stores the brief of the agent the launch token belongs
// to (leo_brief): the whole brief replaces the old one, an empty one clears
// it. Like the progress route it answers errors, since the agent reads them.
// 403 unknown token, 400 invalid brief, 409 briefs are off, 404 no record.
// POST /api/agent/brief
func (s *Server) handleAPIAgentBrief(w http.ResponseWriter, r *http.Request) {
	var req briefRequest
	if !decodeControlBody(w, r, maxBriefBodyBytes, false, &req) {
		return
	}
	name, ok := "", false
	if s.attention != nil {
		name, ok = s.attention.AgentForToken(req.Token)
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "unknown or expired agent token (is this running inside a Leo agent?)"})
		return
	}
	if !s.briefEnabled() {
		writeJSON(w, http.StatusConflict, apiResponse{Error: "agent briefs are off on this machine (brief.enabled in leo.yaml)"})
		return
	}
	in, err := decodeBriefInput(req.Brief)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	next, err := brief.Normalize(in, s.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	stored := &next
	if next.IsZero() {
		stored = nil
	}
	if !s.storeBrief(w, name, stored) {
		return
	}
	s.briefs.Wrote(name)
	if s.publisher != nil {
		s.publisher.Publish(observe.Event{Type: observe.EventAgentBrief, Payload: &observe.AgentBriefPayload{Agent: name, Brief: stored}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(briefResponse{OK: true, Brief: stored})
}

// decodeBriefInput reads the brief object strictly: an unknown field is a
// mistake the agent should hear about, not text silently dropped.
func decodeBriefInput(raw json.RawMessage) (brief.Input, error) {
	var in brief.Input
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return in, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return brief.Input{}, fmt.Errorf("%w: %v", brief.ErrInvalid, err)
	}
	return in, nil
}

// storeBrief persists name's brief, writing the failure response itself.
func (s *Server) storeBrief(w http.ResponseWriter, name string, b *brief.Brief) bool {
	home, ok := s.homePath()
	if !ok {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "config unavailable"})
		return false
	}
	if err := agentstore.SetBrief(home, name, b); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, agentstore.ErrNotFound) {
			status = http.StatusNotFound
		}
		log.Printf("[%s] storing brief: %v", name, err)
		writeJSON(w, status, apiResponse{Error: "could not store the brief: " + err.Error()})
		return false
	}
	return true
}
