package web

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// maxHookPayloadBytes bounds a hook body; harness payloads are small JSON.
const maxHookPayloadBytes = 1 << 20

// agentHookPayload is the subset of a Claude/Codex hook payload that drives
// attention.
type agentHookPayload struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	NotificationType string `json:"notification_type"`
}

// attentionForHook maps one hook event to an attention transition; ok=false
// means the event carries no attention meaning.
func attentionForHook(p agentHookPayload) (observe.AttentionState, bool) {
	switch p.HookEventName {
	case "UserPromptSubmit":
		return observe.AttentionWorking, true
	case "Stop", "Interrupt":
		return observe.AttentionFinished, true
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return observe.AttentionNeedsInput, true
		}
	}
	return "", false
}

// handleAPIAgentHook receives a supervised agent's turn hooks (relayed by
// `leo dispatch report` when LEO_ATTENTION_AGENT is set) and records the
// attention transition. Every well-formed request is acknowledged so the
// hook never retries: an unknown agent, an event with no attention meaning,
// or a payload from a different session is a 2xx no-op.
// POST /api/agent/{name}/hook
func (s *Server) handleAPIAgentHook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxHookPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "reading hook payload"})
		return
	}
	var payload agentHookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid hook payload"})
		return
	}
	state, ok := attentionForHook(payload)
	if ok && s.isAgentHookSession(r.PathValue("name"), payload.SessionID) {
		s.attention.Set(r.PathValue("name"), state)
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// isAgentHookSession reports whether a hook for sessionID belongs to the
// named agent. Codex hooks are home-global and child harness processes
// inherit the agent's env, so a payload naming a session other than the
// agent's known one is some other process's turn. Either id missing (codex
// discovers its id after the first turn) is accepted.
func (s *Server) isAgentHookSession(name, sessionID string) bool {
	if s.agentSvc == nil || s.attention == nil {
		return false
	}
	_, h, ok := s.agentSvc.ResolveHandle(name)
	if !ok {
		return false
	}
	if sessionID == "" || h.IDs == nil {
		return true
	}
	known := h.IDs.Get()
	return known == "" || known == sessionID
}
