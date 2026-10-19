package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"
)

// maxPermissionSummaryRunes caps a permission message's input summary.
const maxPermissionSummaryRunes = 300

// waiters routes answers to the goroutines blocked on ask and permission
// messages. A waiter is settled once; every goroutine waiting on it wakes.
type waiters struct {
	mu sync.Mutex
	m  map[string]*waiter
}

// waiter is one open message's: done closes once it is settled, with value
// (the answer; nil when it ended without one). blocked counts the calls
// waiting on it, guarded by waiters.mu.
type waiter struct {
	done    chan struct{}
	value   *Answer
	blocked int
	settled bool
}

func newWaiters() *waiters { return &waiters{m: map[string]*waiter{}} }

func (w *waiters) add(id string) *waiter {
	w.mu.Lock()
	defer w.mu.Unlock()
	wt := &waiter{done: make(chan struct{})}
	w.m[id] = wt
	return wt
}

func (w *waiters) get(id string) (*waiter, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wt, ok := w.m[id]
	return wt, ok
}

func (w *waiters) remove(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m, id)
}

// join counts a call as blocked on wt. It reports false when wt is already
// settled, which the call then reads from the store instead.
func (w *waiters) join(wt *waiter) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if wt.settled {
		return false
	}
	wt.blocked++
	return true
}

// leave uncounts a call joined to wt and reports whether wt was settled
// while it was counted.
func (w *waiters) leave(wt *waiter) (settled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wt.blocked--
	return wt.settled
}

// settle settles id's waiter with value, removes it, and returns how many
// calls were blocked on it: an answer nobody is blocked on is late. It
// returns 0 when id has no waiter or it was already settled. Never blocks.
func (w *waiters) settle(id string, value *Answer) (blocked int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wt, ok := w.m[id]
	if !ok || wt.settled {
		return 0
	}
	wt.settled = true
	wt.value = value
	close(wt.done)
	delete(w.m, id)
	return wt.blocked
}

// awaitEnd says what an ended wait means for the message: the state and
// answer it is left in when the waiter's context ends first.
type awaitEnd struct {
	state  string
	answer *Answer
	reason string // the closed_reason of an expired ask
}

// closeWaited moves m, whose wait ended, to end's state. An expired ask
// records why it closed, which a plain transition does not.
func (s *Service) closeWaited(m Message, end awaitEnd) (bool, error) {
	if m.Kind == KindAsk && end.state == StateExpired {
		return s.opts.Store.CloseAsk(m.ID, end.reason)
	}
	return s.opts.Store.Transition(m.ID, []string{StateOpen}, end.state, end.answer)
}

// awaitWith stores open message m, announces it, and blocks until it is
// answered (settled, with the answer), times out (expired) or ctx ends (as
// gone says).
func (s *Service) awaitWith(ctx context.Context, m Message, timeout time.Duration, gone awaitEnd) (Answer, bool) {
	m, wt, err := s.insertOpen(m)
	if err != nil {
		s.opts.Logf("chat: storing %s prompt for %s: %v", m.Kind, m.Agent, err)
		return Answer{}, false
	}
	defer s.waiters.remove(m.ID)
	timer := s.opts.After(timeout)
	var end awaitEnd
	select {
	case <-wt.done:
		if wt.value == nil {
			return Answer{}, false
		}
		return *wt.value, true
	case <-timer:
		end = awaitEnd{state: StateExpired, reason: ClosedTimeout}
	case <-s.ctx.Done():
		// Shutdown: nobody can answer it now.
		end = awaitEnd{state: StateExpired, reason: ClosedTimeout}
	case <-ctx.Done():
		end = gone
	}
	ok, err := s.closeWaited(m, end)
	if err != nil {
		s.opts.Logf("chat: closing %s: %v", m.ID, err)
		return Answer{}, false
	}
	if ok {
		s.publishUpdated(m.ID, ChangeState)
		return Answer{}, false
	}
	// An answer won the race: honour it.
	if cur, err := s.opts.Store.Message(m.ID); err == nil && cur.State == StateAnswered && cur.Answer != nil && !cur.Answer.isTerminal() {
		return *cur.Answer, true
	}
	return Answer{}, false
}

// insertOpen stores open message m with a waiter for its answer, put in
// place before the message is stored so no answer arrives unheard, and
// announces it.
func (s *Service) insertOpen(m Message) (Message, *waiter, error) {
	var wt *waiter
	var id string
	m, err := s.insert(m, nil, nil, func(taken string) { id, wt = taken, s.waiters.add(taken) })
	if err != nil {
		if wt != nil {
			s.waiters.remove(id)
		}
		return m, nil, err
	}
	s.publish(EventMessage, messageEvent(m))
	return m, wt, nil
}

// permissionRequest is the part of a PermissionRequest hook payload chat
// shows.
type permissionRequest struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// permissionInput is a permission message's answer control, so clients
// render it like an ask.
var permissionInput = Input{Type: InputButtons, Options: []Option{{Value: "allow", Label: "Allow"}, {Value: "deny", Label: "Deny"}}}

// askUserQuestionTool is the tool whose permission prompt chat shows as a
// questions card rather than an Allow/Deny one.
const askUserQuestionTool = "AskUserQuestion"

// Decision is what a PermissionRequest hook answers Claude with: a behavior
// (allow or deny) and, for an allow, the tool input to run with in place of
// its own. The zero Decision is no decision: the terminal dialog, already
// on screen, stands.
type Decision struct {
	Behavior     string          `json:"behavior,omitempty"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

// IsZero reports whether d is no decision.
func (d Decision) IsZero() bool { return d.Behavior == "" && d.UpdatedInput == nil }

// RequestPermission posts a Claude permission prompt to agent's thread and
// waits up to timeout for the answer from chat: allow or deny, or for an
// AskUserQuestion that parses, the answers to its questions card (an allow
// with the answers as updated input). It returns the zero Decision on
// timeout, dismissal, or when the hook goes away — Claude kills it once the
// prompt is answered in the terminal, which marks the message answered there.
func (s *Service) RequestPermission(ctx context.Context, agent string, payload json.RawMessage, timeout time.Duration) Decision {
	if s.enter() != nil {
		return Decision{}
	}
	defer s.leave()
	var req permissionRequest
	if err := json.Unmarshal(payload, &req); err != nil || req.ToolName == "" {
		return Decision{}
	}
	if req.ToolName == askUserQuestionTool {
		if card, err := parseQuestions(req.ToolInput); err == nil {
			return s.requestQuestions(ctx, agent, card, timeout)
		}
	}
	summary := summarizeToolInput(req.ToolInput)
	m := Message{
		Agent: agent, Direction: DirectionOut, Kind: KindPermission,
		Text: req.ToolName + ": " + summary, State: StateOpen,
		Input:      &permissionInput,
		Permission: &Permission{Tool: req.ToolName, Summary: summary, input: req.ToolInput},
	}
	answer, settled := s.awaitWith(ctx, m, timeout, awaitEnd{state: StateAnswered, answer: terminalAnswer})
	if !settled || len(answer.Values) != 1 || (answer.Values[0] != "allow" && answer.Values[0] != "deny") {
		return Decision{}
	}
	return Decision{Behavior: answer.Values[0]}
}

// requestQuestions posts card as an ask with no permission and waits for its
// answers. Only an answer from chat decides: every other end (timeout,
// dismissal, the hook going away) leaves the terminal's own dialog to stand,
// and nothing is ever denied.
func (s *Service) requestQuestions(ctx context.Context, agent string, card questionsCard, timeout time.Duration) Decision {
	m := Message{Agent: agent, Direction: DirectionOut, Kind: KindAsk, Text: card.Text, State: StateOpen, Input: &card.Input}
	answer, settled := s.awaitWith(ctx, m, timeout, awaitEnd{state: StateAnswered, answer: terminalAnswer})
	if !settled || answer.Answers == nil {
		return Decision{}
	}
	return Decision{Behavior: "allow", UpdatedInput: card.updatedInput(answer)}
}

// summarizeToolInput renders the field of a tool input a person decides
// on: the command, the path or URL, else the input as compact JSON.
func summarizeToolInput(raw json.RawMessage) string {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err == nil {
		for _, key := range []string{"command", "file_path", "notebook_path", "url", "pattern", "path", "query"} {
			if v, ok := fields[key].(string); ok && strings.TrimSpace(v) != "" {
				return clipRunes(strings.TrimSpace(v), maxPermissionSummaryRunes)
			}
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return ""
	}
	return clipRunes(compact.String(), maxPermissionSummaryRunes)
}

// HookEvent is the part of a supervised agent's hook payload chat uses.
type HookEvent struct {
	Event                string          `json:"hook_event_name"`
	TranscriptPath       string          `json:"transcript_path"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
}

// OnHook applies one of agent's turn hooks: a Stop resolves pending
// auto-captures; a PostToolUse for the tool call an open permission prompt
// asked about marks it answered in the terminal.
func (s *Service) OnHook(agent string, ev HookEvent) {
	if s.enter() != nil {
		return
	}
	defer s.leave()
	switch ev.Event {
	case "Stop":
		s.resolveCaptures(agent, ev)
	case "PostToolUse":
		s.settlePermissionInTerminal(agent, ev)
	}
}

func (s *Service) resolveCaptures(agent string, ev HookEvent) {
	captures, err := s.opts.Store.Captures(agent)
	if err != nil {
		s.opts.Logf("chat: reading captures of %s: %v", agent, err)
		return
	}
	if len(captures) == 0 {
		return
	}
	entries := ReadTranscript(ev.TranscriptPath)
	outcomes, mark := ResolveCaptures(entries, captures, ev.LastAssistantMessage, s.opts.Now(), s.marks.get(agent))
	s.marks.advance(agent, mark)
	for _, out := range outcomes {
		if out.Action == CaptureKeep {
			continue
		}
		removed, err := s.opts.Store.RemoveCapture(out.Capture.MessageID)
		if err != nil {
			s.opts.Logf("chat: clearing capture %s: %v", out.Capture.MessageID, err)
			continue
		}
		if removed && out.Action == CapturePost {
			s.postReply(agent, out.Capture.MessageID, out.Text)
		}
	}
}

// captureMarks holds each agent's capture mark: the stamp of the last
// prompt occurrence a capture answered. It lives in memory; after a
// restart the delivery time alone separates repeats.
type captureMarks struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (c *captureMarks) get(agent string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[agent]
}

// advance moves agent's mark forward only, as concurrent Stop hooks may
// finish out of order.
func (c *captureMarks) advance(agent string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.m[agent]) {
		c.m[agent] = t
	}
}

// postReply posts an auto-captured reply, threaded to the message it
// answers.
func (s *Service) postReply(agent, replyTo, text string) {
	if len(text) > MaxTextBytes {
		text = clipRunes(text, MaxTextBytes/4)
	}
	m, err := s.insert(Message{Agent: agent, Direction: DirectionOut, Kind: KindText, Text: text, ReplyTo: replyTo}, nil, nil, nil)
	if err != nil {
		s.opts.Logf("chat: posting reply for %s: %v", agent, err)
		return
	}
	s.publish(EventMessage, messageEvent(m))
}

func (s *Service) settlePermissionInTerminal(agent string, ev HookEvent) {
	open, err := s.opts.Store.OpenMessages(agent, KindPermission, StateOpen)
	if err != nil {
		s.opts.Logf("chat: reading open permission prompts of %s: %v", agent, err)
		return
	}
	for _, m := range open {
		if m.Permission == nil || m.Permission.Tool != ev.ToolName || !sameJSON(m.Permission.input, ev.ToolInput) {
			continue
		}
		ok, err := s.opts.Store.Transition(m.ID, []string{StateOpen}, StateAnswered, terminalAnswer)
		if err != nil || !ok {
			continue
		}
		s.waiters.settle(m.ID, nil)
		s.publishUpdated(m.ID, ChangeAnswer)
		return
	}
}

// sameJSON reports whether two JSON documents are equal as values.
func sameJSON(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
