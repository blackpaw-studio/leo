package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// interruptDelayedAttempts / interruptDelayedPoll bound
// handleWebAgentInterrupt's background delayed-Escape burst
// (~interruptDelayedAttempts*interruptDelayedPoll ≈ 2.5s) so it keeps
// catching state transitions for a few seconds after the immediate burst.
// Package vars (not consts) so tests can shrink them to keep the delayed
// burst fast.
var (
	interruptDelayedAttempts = 5
	interruptDelayedPoll     = 500 * time.Millisecond
)

// handleWebAgentInterrupt interrupts whatever an agent is currently doing
// (see interruptAgent) and answers with a flash.
//
// POST /web/agent/{name}/interrupt
func (s *Server) handleWebAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if out := s.interruptAgent(r.Context(), name); out.err != "" {
		s.renderFlashStatus(w, out.status, "error", out.err)
		return
	}
	s.renderFlash(w, "success", fmt.Sprintf("Interrupted %s", name))
}

// handleWebAgentSendKeys sends arbitrary keys/text to an agent's tmux session.
// POST /web/agent/{name}/send  {"keys": ["/clear", "Enter"]}
//
// Multi-char literal strings (e.g. "/clear") are split into individual
// keystrokes with a small inter-key delay. Claude Code's Ink-based REPL
// treats rapid bulk send-keys as pasted text and won't activate slash-command
// menus; per-char sends make each key register as a real keypress.
func (s *Server) handleWebAgentSendKeys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := agent.SessionName(name)

	var req struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "keys is required"})
		return
	}

	if err := s.typeKeys(sessionName, req.Keys); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// typeKeys types keys into session's pane, splitting multi-char literals
// into individual keystrokes (see handleWebAgentSendKeys).
func (s *Server) typeKeys(sessionName string, keys []string) error {
	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)
	for _, key := range keys {
		if needsCharSplit(key) {
			for _, ch := range key {
				if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, string(ch))...).Run(); err != nil {
					return fmt.Errorf("send-keys failed: %w", err)
				}
				time.Sleep(30 * time.Millisecond)
			}
			continue
		}
		if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, key)...).Run(); err != nil {
			return fmt.Errorf("send-keys failed: %w", err)
		}
	}
	return nil
}

// handleWebAgentCompact compacts an agent's conversation.
// POST /web/agent/{name}/compact
func (s *Server) handleWebAgentCompact(w http.ResponseWriter, r *http.Request) {
	writeWebControl(w, s.slashCommandAgent(r.PathValue("name"), "compact", "", bridge.Compact("")))
}

// handleWebAgentClear clears an agent's conversation.
// POST /web/agent/{name}/clear
func (s *Server) handleWebAgentClear(w http.ResponseWriter, r *http.Request) {
	writeWebControl(w, s.slashCommandAgent(r.PathValue("name"), "clear", "", bridge.Clear()))
}

// writeWebControl renders a control outcome in the /web/agent JSON shape:
// the envelope alone, plus {queued: true} on a 202 that is the bridge
// holding the command.
func writeWebControl(w http.ResponseWriter, out controlOutcome) {
	if out.err != "" {
		writeJSON(w, out.status, apiResponse{Error: out.err})
		return
	}
	resp := apiResponse{OK: true}
	if out.bridgeQueued {
		resp.Data = map[string]bool{"queued": true}
	}
	writeJSON(w, out.status, resp)
}

// publishAgentMessage announces that from messaged to, as a pair of names and
// nothing else — the routed body never reaches the event (see
// observe.AgentMessagePayload). from is empty when the sender is not an agent
// (a human using the web UI); leo does not invent a sender, and the field is
// omitted on the wire so consumers can require it.
//
// Called only once a message has been accepted for delivery, so a rejected
// send announces nothing. A nil publisher makes this a no-op.
func (s *Server) publishAgentMessage(from, to string) {
	if s.publisher == nil {
		return
	}
	s.publisher.Publish(observe.Event{
		Type:    observe.EventAgentMessage,
		Payload: &observe.AgentMessagePayload{From: from, To: to},
	})
}

// sessionInputWait bounds how long the live typed path waits for another
// delivery into the session to finish: under the MCP client's 30 s timeout
// and the server's WriteTimeout, so the sender hears "busy" rather than
// timing out on a message that may still be typed after it left. A var so
// tests can shorten it.
var sessionInputWait = 20 * time.Second

// handleWebAgentMessage delivers a free-text message into an agent's live
// Claude prompt and submits it. Unlike handleWebAgentSendKeys (which types
// char-by-char to drive slash-command menus), this sends the body verbatim
// with `send-keys -l` so arbitrary text — including tmux key names like
// "Enter" or "C-c" — is typed literally, then submits with a separate Enter.
//
// POST /web/agent/{name}/message  {"text": "hello"}
func (s *Server) handleWebAgentMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Text string `json:"text"`
		// From identifies the sending agent. Supplied by the leo_send_message
		// MCP tool from its own LEO_PROCESS_NAME; absent when a human sends
		// from the web UI. Self-asserted — display only, never authorization.
		From string `json:"from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "text is required"})
		return
	}

	writeWebControl(w, s.messageAgent(r.Context(), name, req.From, req.Text))
}

// resolveMessageTarget resolves name to its harness name and SessionHandle,
// trying the agent resolver first (agentstore-backed) and falling back to
// the generic resolveHandle seam. ok=false means neither resolver claims the
// name — the caller falls back to the existing tmux/claude logic, which does
// its own "unknown target" check.
func (s *Server) resolveMessageTarget(name string) (harnessName string, h harness.SessionHandle, ok bool) {
	if s.agentSvc != nil {
		if hn, handle, resolved := s.agentSvc.ResolveHandle(name); resolved {
			return hn, handle, true
		}
	}
	if s.resolveHandle != nil {
		if hn, handle, resolved := s.resolveHandle(name); resolved {
			return hn, handle, true
		}
	}
	return "", harness.SessionHandle{}, false
}

// nonClaudeInjectTimeout bounds a non-claude driver's Inject call (a
// readiness-probed tmux paste, not a synchronous turn — Inject returns as
// soon as the message lands in the pane) so a wedged pane or hung probe loop
// can't block the web handler indefinitely. Generous because the readiness
// probe itself may need to wait out a busy TUI before it can paste.
const nonClaudeInjectTimeout = 5 * time.Minute

// messageInputAttempts / messageInputPoll bound how long
// handleWebAgentMessage waits for typed text to surface in claude's input
// box before submitting. ~messageInputAttempts*messageInputPoll (≈2s) is
// ample for an already-running session to echo input; package vars so tests
// can shrink them.
var (
	messageInputAttempts = 40
	messageInputPoll     = 50 * time.Millisecond
)

// waitForInputContent polls pane until the input box carries the just-typed
// text, then returns. Falls open after the attempt budget so a busy or
// unreadable pane never blocks (or drops) the submit.
func (s *Server) waitForInputContent(tmuxPath, pane string) {
	for i := 0; i < messageInputAttempts; i++ {
		out, err := s.execCommand(tmuxPath, tmux.Args("capture-pane", "-p", "-t", pane)...).Output()
		if err == nil && tmux.PaneInputHasContent(string(out)) {
			return
		}
		time.Sleep(messageInputPoll)
	}
}

// resolvePaneTarget resolves session's concrete pane via list-panes, falling
// back to tmux.PaneTarget(session)'s active-pane selector if resolution
// fails — best-effort, the same posture as internal/tmux's
// devchannel/abort/dismiss-dialog paths. Routed through s.execCommand
// (rather than tmux.ResolvePaneOrFallback, which uses tmux's own internal
// exec seam keyed on context.Context) so tests can control tmux output
// through the same seam the rest of this package already uses.
func (s *Server) resolvePaneTarget(tmuxPath, session string) string {
	out, err := s.execCommand(tmuxPath, tmux.Args("list-panes", "-t", tmux.PaneTarget(session), "-F", "#{pane_id}")...).Output()
	if err == nil {
		if pane, perr := tmux.LowestPaneID(string(out)); perr == nil {
			return pane
		}
	}
	return tmux.PaneTarget(session)
}

// needsCharSplit reports whether a send-keys arg is a multi-char literal
// string that should be typed one character at a time. Single chars and
// tmux key names (Enter, Escape, BSpace, F1, C-u, M-a, …) are sent as one
// keypress. Heuristic: key names begin with an uppercase letter, literals
// do not.
func needsCharSplit(s string) bool {
	if len(s) <= 1 {
		return false
	}
	r := rune(s[0])
	return r < 'A' || r > 'Z'
}
