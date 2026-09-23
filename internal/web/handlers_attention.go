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

// agentHookRequest is what `leo dispatch report` POSTs for a supervised
// agent: the launch's attention token plus the raw hook payload.
type agentHookRequest struct {
	Token   string           `json:"token"`
	Payload agentHookPayload `json:"payload"`
}

// hookTransition is one hook event's attention meaning. from, when set,
// limits it to agents currently in one of those states.
type hookTransition struct {
	state observe.AttentionState
	from  []observe.AttentionState
}

// attentionForHook maps one hook event to an attention transition; ok=false
// means the event carries no attention meaning.
func attentionForHook(p agentHookPayload) (hookTransition, bool) {
	switch p.HookEventName {
	case "UserPromptSubmit":
		return hookTransition{state: observe.AttentionWorking}, true
	case "PostToolUse":
		// A tool ran, so a permission prompt or elicitation was answered.
		// Only that clears; any other state is left alone, unbumped.
		return hookTransition{state: observe.AttentionWorking, from: []observe.AttentionState{observe.AttentionNeedsInput}}, true
	case "Stop", "Interrupt":
		return hookTransition{state: observe.AttentionFinished}, true
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return hookTransition{state: observe.AttentionNeedsInput}, true
		}
	}
	return hookTransition{}, false
}

// handleAPIAgentHook receives a supervised agent's turn hooks (relayed by
// `leo dispatch report` when LEO_ATTENTION_TOKEN is set) and records the
// attention transition for the agent the per-launch token currently belongs
// to, so a rename follows the agent and a stopped launch's hooks go nowhere.
// Every well-formed request is acknowledged so the hook never retries: an
// unknown or stale token, an event with no attention meaning, or a payload
// from a different session is a 2xx no-op.
// POST /api/agent/hook
func (s *Server) handleAPIAgentHook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxHookPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "reading hook payload"})
		return
	}
	var req agentHookRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid hook payload"})
		return
	}
	tr, ok := attentionForHook(req.Payload)
	if ok {
		// The session check needs the name; SetByToken re-resolves the
		// token atomically, so a stop or rename in between still wins.
		if name, known := s.attention.AgentForToken(req.Token); known && s.isAgentHookSession(name, req.Payload.SessionID) {
			s.attention.SetByToken(req.Token, tr.state, tr.from...)
		}
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
