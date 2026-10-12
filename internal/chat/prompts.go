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
// messages.
type waiters struct {
	mu sync.Mutex
	ch map[string]chan string
}

func newWaiters() *waiters { return &waiters{ch: map[string]chan string{}} }

func (w *waiters) add(id string) chan string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan string, 1)
	w.ch[id] = ch
	return ch
}

func (w *waiters) remove(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.ch, id)
}

// settle hands value to id's waiter, if any. Never blocks.
func (w *waiters) settle(id, value string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.ch[id]; ok {
		select {
		case ch <- value:
		default:
		}
	}
}

// awaitEnd says what an ended wait means for the message: the state and
// answer it is left in when the waiter's context ends first.
type awaitEnd struct {
	state  string
	answer string
}

// await stores open message m, announces it, and blocks until it is
// answered (settled, with the answer), times out or ctx ends (both leave
// it settled=false: expired, or as gone says).
func (s *Service) await(ctx context.Context, m Message, timeout time.Duration) (string, bool) {
	return s.awaitWith(ctx, m, timeout, awaitEnd{state: StateExpired})
}

func (s *Service) awaitWith(ctx context.Context, m Message, timeout time.Duration, gone awaitEnd) (string, bool) {
	ch := s.waiters.add(m.ID)
	defer s.waiters.remove(m.ID)
	if err := s.opts.Store.InsertMessage(m); err != nil {
		s.opts.Logf("chat: storing %s prompt for %s: %v", m.Kind, m.Agent, err)
		return "", false
	}
	s.publish(EventMessage, &MessagePayload{Message: m})
	timer := s.opts.After(timeout)
	var end awaitEnd
	select {
	case v := <-ch:
		return v, v != ""
	case <-timer:
		end = awaitEnd{state: StateExpired}
	case <-ctx.Done():
		end = gone
	}
	ok, err := s.opts.Store.Transition(m.ID, []string{StateOpen}, end.state, end.answer)
	if err != nil {
		s.opts.Logf("chat: closing %s: %v", m.ID, err)
		return "", false
	}
	if ok {
		s.publishUpdated(m.ID, ChangeState)
		return "", false
	}
	// An answer won the race: honour it.
	if cur, err := s.opts.Store.Message(m.ID); err == nil && cur.State == StateAnswered && cur.Answer != AnswerTerminal {
		return cur.Answer, true
	}
	return "", false
}

// permissionRequest is the part of a PermissionRequest hook payload chat
// shows.
type permissionRequest struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// RequestPermission posts a Claude permission prompt to agent's thread and
// waits up to timeout for allow or deny from chat. It returns "" (no
// decision: the terminal dialog, already on screen, stands) on timeout, or
// when the hook goes away — Claude kills it once the prompt is answered in
// the terminal, which marks the message answered there.
func (s *Service) RequestPermission(ctx context.Context, agent string, payload json.RawMessage, timeout time.Duration) string {
	var req permissionRequest
	if err := json.Unmarshal(payload, &req); err != nil || req.ToolName == "" {
		return ""
	}
	summary := summarizeToolInput(req.ToolInput)
	m := Message{
		ID: s.opts.IDs.New(), Agent: agent, Direction: DirectionOut, Kind: KindPermission,
		Text: req.ToolName + ": " + summary, State: StateOpen, TS: s.opts.Now(),
		Permission: &Permission{Tool: req.ToolName, Summary: summary, input: req.ToolInput},
	}
	answer, settled := s.awaitWith(ctx, m, timeout, awaitEnd{state: StateAnswered, answer: AnswerTerminal})
	if !settled || (answer != "allow" && answer != "deny") {
		return ""
	}
	return answer
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
	for _, out := range ResolveCaptures(entries, captures, ev.LastAssistantMessage, s.opts.Now()) {
		if out.Action == CaptureKeep {
			continue
		}
		if err := s.opts.Store.RemoveCapture(out.Capture.MessageID); err != nil {
			s.opts.Logf("chat: clearing capture %s: %v", out.Capture.MessageID, err)
			continue
		}
		if out.Action == CapturePost {
			s.postReply(agent, out.Capture.MessageID, out.Text)
		}
	}
}

// postReply posts an auto-captured reply, threaded to the message it
// answers.
func (s *Service) postReply(agent, replyTo, text string) {
	if len(text) > MaxTextBytes {
		text = clipRunes(text, MaxTextBytes/4)
	}
	m := Message{ID: s.opts.IDs.New(), Agent: agent, Direction: DirectionOut, Kind: KindText, Text: text, ReplyTo: replyTo, TS: s.opts.Now()}
	if err := s.opts.Store.InsertMessage(m); err != nil {
		s.opts.Logf("chat: posting reply for %s: %v", agent, err)
		return
	}
	s.publish(EventMessage, &MessagePayload{Message: m})
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
		ok, err := s.opts.Store.Transition(m.ID, []string{StateOpen}, StateAnswered, AnswerTerminal)
		if err != nil || !ok {
			continue
		}
		s.waiters.settle(m.ID, "")
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
