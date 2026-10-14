package daemon

import "strings"

// TurnEvent is one turn hook from a supervised agent's harness, relayed by
// the web server's agent hook endpoint. Event is the hook_event_name
// (UserPromptSubmit, Stop, Interrupt, StopFailure); anything else is
// ignored.
type TurnEvent struct {
	Event       string
	Prompt      string // UserPromptSubmit: the submitted text
	TurnID      string // the harness's turn id, when it has one (codex)
	SessionID   string
	LastMessage string // Stop: the turn's final assistant message
	Error       string // StopFailure: why the turn failed
}

// boundTurn ties the in-flight invocation to the harness turn whose
// submitted prompt matched it.
type boundTurn struct {
	invID     string
	turnID    string
	sessionID string
}

// ends reports whether a Stop/Interrupt for (turnID, sessionID) closes this
// turn. Turn ids must agree (both empty for a harness without them); a
// differing session is a child session the turn spawned, whose end is not
// the turn's.
func (b *boundTurn) ends(invID string, ev TurnEvent) bool {
	if b == nil || b.invID != invID || b.turnID != ev.TurnID {
		return false
	}
	return b.sessionID == "" || ev.SessionID == "" || b.sessionID == ev.SessionID
}

// ObserveTurn completes session's in-flight invocation from its agent's own
// turn hooks: the harness-neutral completion signal for every harness that
// reports turns (codex and opencode have no task-report Stop hook; claude's
// task-report and this path race harmlessly, the loser is dropped as late).
//
// A UserPromptSubmit whose prompt matches the in-flight invocation's prompt
// (whitespace-normalized) binds that turn. The next Stop for the same turn
// id and session completes it OK; an Interrupt fails it as "interrupted",
// and a StopFailure (the turn ended on an error) as "turn failed".
// Prompts a human types into the pane never match, so their turns are
// ignored, and a session with nothing in flight drops every event.
func (r *sessionRouter) ObserveTurn(session string, ev TurnEvent) {
	q := r.queueFor(session)
	if q == nil {
		return
	}
	q.mu.Lock()
	inv := q.inFlight
	if inv == nil {
		q.mu.Unlock()
		return
	}
	var result InvocationResult
	switch ev.Event {
	case "UserPromptSubmit":
		if q.turn == nil || q.turn.invID != inv.ID {
			if normalizePrompt(ev.Prompt) == normalizePrompt(inv.Prompt) {
				q.turn = &boundTurn{invID: inv.ID, turnID: ev.TurnID, sessionID: ev.SessionID}
			}
		}
		q.mu.Unlock()
		return
	case "Stop":
		result = InvocationResult{OK: true, SessionID: ev.SessionID, FinalMessage: ev.LastMessage}
	case "Interrupt":
		result = InvocationResult{OK: false, SessionID: ev.SessionID, Err: "interrupted"}
	case "StopFailure":
		result = InvocationResult{OK: false, SessionID: ev.SessionID, Err: turnFailure(ev.Error)}
	default:
		q.mu.Unlock()
		return
	}
	if !q.turn.ends(inv.ID, ev) {
		q.mu.Unlock()
		return
	}
	q.turn = nil
	q.inFlight = nil
	// The turn ran to its end: the pump must not treat it as a zombie
	// injection to abort if this lands while it is still inside the injector.
	inv.turnCompleted = true
	q.mu.Unlock()

	r.markCompleted(inv, result)
	select {
	case q.reportSig <- struct{}{}:
	default:
	}
}

// normalizePrompt folds whitespace runs, since a TUI paste may rewrap or
// trim the prompt it echoes back in its hook.
func normalizePrompt(text string) string { return strings.Join(strings.Fields(text), " ") }

// turnFailure is a StopFailure's invocation error.
func turnFailure(reason string) string {
	if reason == "" {
		return "turn failed"
	}
	return "turn failed: " + reason
}
