package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
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

// handleWebAgentInterrupt sends a burst of Escape keys into an agent's tmux
// session to interrupt whatever it's currently doing. Escapes are sent
// immediately (to catch the common case) and then repeated in the
// background for a few seconds to catch state transitions (e.g. a tool call
// that completes mid-interrupt and re-arms the input prompt).
//
// POST /web/agent/{name}/interrupt
func (s *Server) handleWebAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := agent.SessionName(name)

	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)
	escArgs := tmux.Args("send-keys", "-t", pane, "Escape")
	// Send Escape immediately, then keep sending to catch state transitions.
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	// Also send delayed Escapes in background to catch tool completions. This
	// spans up to ~2.5s, long enough for a crash-restart to tear down and
	// recreate the session mid-burst — re-resolve the pane before each
	// delayed send rather than reusing the request-entry resolution, or a
	// dead pane ID silently no-ops for the rest of the burst.
	go func() {
		for i := 0; i < interruptDelayedAttempts; i++ {
			time.Sleep(interruptDelayedPoll)
			delayedPane := s.resolvePaneTarget(tmuxPath, sessionName)
			delayedArgs := tmux.Args("send-keys", "-t", delayedPane, "Escape")
			s.execCommand(tmuxPath, delayedArgs...).Run() //nolint:errcheck
		}
		if s.afterInterruptBurst != nil {
			s.afterInterruptBurst()
		}
	}()
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

	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)
	for _, key := range req.Keys {
		if needsCharSplit(key) {
			for _, ch := range key {
				if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, string(ch))...).Run(); err != nil {
					writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
					return
				}
				time.Sleep(30 * time.Millisecond)
			}
			continue
		}
		if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, key)...).Run(); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
			return
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
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

	// Resolve the target's harness FIRST, before any tmux-touching logic.
	// Claude targets (harnessName == "" from an unresolved/claude target)
	// fall straight through to the existing fast-path / suspended-resume
	// logic below, byte-identical to before this change. A resolved
	// non-claude target is routed to its SessionDriver and returns
	// immediately — it never touches tmux, and never suspends (sweep skips
	// non-claude records), so there is no resume branch to consider for it.
	if harnessName, handle, ok := s.resolveMessageTarget(name); ok && harnessName != "" && harnessName != "claude" {
		if s.dispatchNonClaudeMessage(w, harnessName, handle, req.Text) {
			s.publishAgentMessage(req.From, name)
		}
		return
	}

	// Validate the target against running sessions (agents). If the agent is
	// not live but is a suspended agent, resume it first and deliver via the
	// readiness-probing path (InjectPrompt) — a just-resumed claude takes
	// tens of seconds to boot before its input box accepts input, so the 2s
	// fast-path below would silently drop the message.
	//
	// NOTE: a concurrent sweep suspend can race here and make the live send-keys
	// path 500; the sender retries and auto-wakes again.
	states := s.processes.States()
	if _, ok := states[name]; !ok {
		if s.agentSvc != nil {
			rec, err := s.agentSvc.Resume(name)
			if err != nil {
				// Not a suspended agent — unknown target.
				names := make([]string, 0, len(states))
				for n := range states {
					names = append(names, n)
				}
				sort.Strings(names)
				writeJSON(w, http.StatusNotFound, apiResponse{
					Error: fmt.Sprintf("no such agent %q; running: %s", name, strings.Join(names, ", ")),
				})
				return
			}
			// Resumed successfully. A cold-booting claude can take ~60s to load
			// plugins/MCP before its input box accepts input — longer than the
			// server's WriteTimeout — and the readiness-probing injector blocks
			// for that whole window. Deliver asynchronously on a detached context
			// (r.Context() is cancelled once this handler returns) and respond
			// now, so the caller isn't held on the connection and won't
			// false-timeout and retry into a duplicate message.
			const wakeDeliverTimeout = 3 * time.Minute
			sessionName := agent.SessionName(rec.Name)
			body := req.Text
			from := req.From
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), wakeDeliverTimeout)
				defer cancel()
				if err := s.injectPrompt(ctx, sessionName, body); err != nil {
					log.Printf("web: async message delivery after resume of %q failed: %v", sessionName, err)
					return
				}
				// Announced from inside the goroutine, once delivery actually
				// succeeded — not at 202-accept time. The 202 only means the
				// message was queued; this cold-boot path can still fail
				// minutes later, and announcing early would tell a consumer
				// two agents were talking when nothing was ever delivered.
				// Every delivery path therefore announces on delivery, never
				// on acceptance.
				s.publishAgentMessage(from, name)
			}()
			writeJSON(w, http.StatusAccepted, apiResponse{OK: true})
			return
		}
		names := make([]string, 0, len(states))
		for n := range states {
			names = append(names, n)
		}
		sort.Strings(names)
		writeJSON(w, http.StatusNotFound, apiResponse{
			Error: fmt.Sprintf("no such agent %q; running: %s", name, strings.Join(names, ", ")),
		})
		return
	}

	// Live (already-running) fast path: literal paste + readiness confirmation + Enter.
	sessionName := agent.SessionName(name)
	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)

	// Literal paste of the message body.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, "-l", req.Text)...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send message failed: %v", err)})
		return
	}

	// Wait until the input box reflects the typed text before submitting.
	// Claude's Ink REPL batches stdin; an Enter that lands in the same input
	// burst as the literal text is treated as a newline, not a submit, leaving
	// the message unsent (the intermittent "Enter not registered" bug).
	// Confirming the text rendered forces Enter to arrive as a discrete
	// keypress. Bounded, and falls open if the pane never confirms (busy
	// mid-turn or unreadable) so a message is never silently dropped.
	s.waitForInputContent(tmuxPath, pane)

	// Separate Enter to submit.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, "Enter")...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("submit message failed: %v", err)})
		return
	}

	s.publishAgentMessage(req.From, name)
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
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

// dispatchNonClaudeMessage delivers text to a non-claude session via its
// SessionDriver's Inject and never touches tmux. Used by
// handleWebAgentMessage once the target's harness has been resolved to
// something other than claude.
// dispatchNonClaudeMessage delivers via the target's SessionDriver, writing
// the HTTP response itself. It reports whether the message was delivered, so
// the caller can announce it — a failed send must announce nothing.
func (s *Server) dispatchNonClaudeMessage(w http.ResponseWriter, harnessName string, h harness.SessionHandle, text string) bool {
	hd, err := harness.Get(harnessName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("resolving harness %q: %v", harnessName, err)})
		return false
	}
	drv := hd.Driver()
	if drv == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("harness %q has no session driver", harnessName)})
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), nonClaudeInjectTimeout)
	defer cancel()
	if _, err := drv.Inject(ctx, h, text); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("delivering message: %v", err)})
		return false
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
	return true
}

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
