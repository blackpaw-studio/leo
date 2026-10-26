package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"

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
	// AgentID is set when the hook fired inside a subagent rather than the
	// main loop.
	AgentID string `json:"agent_id"`
	// ToolName and ToolInput describe the call a PermissionRequest asks about.
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	// MCPServerName names the server an Elicitation hook comes from.
	MCPServerName string `json:"mcp_server_name"`
	// LastAssistantMessage is the turn's final message, on a Stop hook.
	LastAssistantMessage string `json:"last_assistant_message"`
	// Prompt is the submitted text, on a UserPromptSubmit hook.
	Prompt string `json:"prompt"`
	// TurnID is the harness's turn id (codex sends one on every turn hook).
	TurnID string `json:"turn_id"`
	// Error is why the turn failed, on a StopFailure hook. Raw so a
	// harness that sends an object instead of a string is not a 400.
	Error json.RawMessage `json:"error"`
}

// hookError renders a hook's error field: a JSON string as its value, any
// other JSON as compact text.
func hookError(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// AgentTurn is the turn-relevant part of one supervised agent hook, handed
// to the turn observer (see WithTurnObserver).
type AgentTurn struct {
	Event       string // hook_event_name
	Prompt      string // UserPromptSubmit: the submitted text
	TurnID      string // the harness's turn id, when it has one
	SessionID   string
	LastMessage string // Stop: the turn's final assistant message
	Error       string // StopFailure: why the turn failed
}

// agentHookRequest is what `leo dispatch report` POSTs for a supervised
// agent: the launch's attention token plus the raw hook payload.
type agentHookRequest struct {
	Token   string          `json:"token"`
	Payload json.RawMessage `json:"payload"`
}

// hookTransition is one hook event's attention meaning. from, when set,
// limits it to agents currently in one of those states.
type hookTransition struct {
	state observe.AttentionState
	from  []observe.AttentionState
	// reason, set with needs_input, says what the agent is waiting on.
	reason *observe.AttentionReason
}

// attentionForHook maps one hook event to an attention transition; ok=false
// means the event carries no attention meaning. Events that describe one
// tool call or prompt only count from the main loop (no agent_id), as the
// claude mod's classic.* handlers did.
func attentionForHook(p agentHookPayload, home string) (hookTransition, bool) {
	switch p.HookEventName {
	case "UserPromptSubmit":
		return hookTransition{state: observe.AttentionWorking}, true
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		// A tool ran or was refused, so a permission prompt or elicitation
		// was answered. Only that clears; any other state is left alone,
		// unbumped.
		if p.AgentID != "" {
			return hookTransition{}, false
		}
		return hookTransition{state: observe.AttentionWorking, from: []observe.AttentionState{observe.AttentionNeedsInput}}, true
	case "PermissionRequest":
		// A question is the bridge's to raise (kind question, with its text).
		if p.AgentID != "" || p.ToolName == "AskUserQuestion" {
			return hookTransition{}, false
		}
		tool, summary := observe.ToolActivity(p.ToolName, p.ToolInput, home)
		return needsInput(observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: tool, Detail: summary}), true
	case "Elicitation":
		if p.AgentID != "" {
			return hookTransition{}, false
		}
		server, _ := observe.ToolActivity(p.MCPServerName, nil, home)
		return needsInput(observe.AttentionReason{Kind: observe.AttentionReasonElicitation, Detail: server}), true
	case "Stop", "Interrupt", "StopFailure":
		return hookTransition{state: observe.AttentionFinished}, true
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return hookTransition{state: observe.AttentionNeedsInput}, true
		}
	}
	return hookTransition{}, false
}

func needsInput(reason observe.AttentionReason) hookTransition {
	return hookTransition{state: observe.AttentionNeedsInput, reason: &reason}
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
	var payload agentHookPayload
	if err := json.Unmarshal(raw, &req); err != nil || (len(req.Payload) > 0 && json.Unmarshal(req.Payload, &payload) != nil) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid hook payload"})
		return
	}
	name, known := "", false
	if s.attention != nil {
		name, known = s.attention.AgentForToken(req.Token)
	}
	ours := known && s.isAgentHookSession(name, payload.SessionID)
	home, _ := os.UserHomeDir()
	if tr, ok := attentionForHook(payload, home); ok && ours {
		// The SetByToken family re-resolves the token atomically, so a stop
		// or rename since the name was read still wins.
		switch {
		case payload.HookEventName == "Stop":
			s.attention.SetByTokenFinished(req.Token, payload.LastAssistantMessage)
		case tr.reason != nil:
			s.attention.SetByTokenNeedsInput(req.Token, *tr.reason)
		default:
			s.attention.SetByToken(req.Token, tr.state, tr.from...)
		}
	}
	if ours {
		s.chatHook(name, req.Payload)
		if s.turnObserver != nil {
			s.turnObserver(name, AgentTurn{
				Event:       payload.HookEventName,
				Prompt:      payload.Prompt,
				TurnID:      payload.TurnID,
				SessionID:   payload.SessionID,
				LastMessage: payload.LastAssistantMessage,
				Error:       hookError(payload.Error),
			})
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
