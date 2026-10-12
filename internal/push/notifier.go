package push

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

const defaultQueueSize = 64

// Notifier is an observe.Publisher decorator: it forwards every event to
// next, and on each agent's attention transition into a needs-you state
// queues an Alert for its Sender. The queue is bounded and Publish never
// waits on it, so a slow or dead relay cannot stall the attention path; an
// alert that finds the queue full is dropped.
type Notifier struct {
	next   observe.Publisher
	sender Sender
	now    func() time.Time
	queue  chan Alert
	// allow decides, per needs-you transition, whether to queue an alert.
	// Nil sends every one.
	allow func(agent, state string) bool

	mu   sync.Mutex
	last map[string]observe.AttentionState
	// previews holds each agent's latest turn preview until the finished
	// transition it precedes consumes it. The bridge feed publishes the
	// turn_completed event before advancing attention to finished.
	previews map[string]string
	// openPermissions holds each agent's open Leo Chat permission message,
	// whose id rides that agent's permission attention alert.
	openPermissions map[string]string
}

// NotifierOption configures a Notifier.
type NotifierOption func(*Notifier)

// WithQueueSize bounds the pending-alert queue.
func WithQueueSize(n int) NotifierOption {
	return func(nt *Notifier) {
		if n > 0 {
			nt.queue = make(chan Alert, n)
		}
	}
}

// WithNotifierClock sets the clock stamping each alert.
func WithNotifierClock(now func() time.Time) NotifierOption {
	return func(nt *Notifier) { nt.now = now }
}

// WithPolicy sets the filter consulted on each needs-you transition with the
// agent name and attention state; the alert is queued only when it returns
// true. It runs on the publisher's goroutine, so it must be quick and safe
// for concurrent use. Without a policy every needs-you state sends.
func WithPolicy(allow func(agent, state string) bool) NotifierOption {
	return func(nt *Notifier) { nt.allow = allow }
}

// NewNotifier wraps next (nil forwards nowhere) and delivers through sender
// once Start has run.
func NewNotifier(next observe.Publisher, sender Sender, opts ...NotifierOption) *Notifier {
	n := &Notifier{
		next:   next,
		sender: sender,
		now:    time.Now,
		queue:  make(chan Alert, defaultQueueSize),
		last:   make(map[string]observe.AttentionState),

		previews:        make(map[string]string),
		openPermissions: make(map[string]string),
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// Start runs the delivery worker until ctx ends. Alerts are delivered one
// at a time, in order.
func (n *Notifier) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case a := <-n.queue:
				n.sender.Send(ctx, a)
			}
		}
	}()
}

// Publish forwards ev, then inspects it for an attention transition.
func (n *Notifier) Publish(ev observe.Event) {
	if n.next != nil {
		n.next.Publish(ev)
	}
	switch p := ev.Payload.(type) {
	case *observe.AgentActivityPayload:
		if p.Attention != nil {
			n.observe(p.Agent, *p.Attention)
		}
	case *observe.AgentTurnCompletedPayload:
		n.mu.Lock()
		n.previews[p.Agent] = p.Preview
		n.mu.Unlock()
	case *observe.AgentStoppedPayload:
		n.mu.Lock()
		delete(n.last, p.Agent)
		delete(n.previews, p.Agent)
		delete(n.openPermissions, p.Agent)
		n.mu.Unlock()
	case *chat.MessagePayload:
		n.trackPermission(p.Message)
		n.chatMessage(p.Message)
	case *chat.UpdatedPayload:
		n.trackPermission(p.Message)
	}
}

// trackPermission keeps openPermissions current with a permission
// message's state.
func (n *Notifier) trackPermission(m chat.Message) {
	if m.Kind != chat.KindPermission {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case m.State == chat.StateOpen:
		n.openPermissions[m.Agent] = m.ID
	case n.openPermissions[m.Agent] == m.ID:
		delete(n.openPermissions, m.Agent)
	}
}

// chatMessage queues a chat push for an agent's outbound message. Permission
// prompts push through attention (with their message id) and system notes
// not at all, so each event has one push source.
func (n *Notifier) chatMessage(m chat.Message) {
	if m.Direction != chat.DirectionOut || m.Kind == chat.KindPermission || m.Kind == chat.KindSystem {
		return
	}
	if n.allow != nil && !n.allow(m.Agent, config.PushStateChat) {
		return
	}
	a := Alert{Agent: m.Agent, Kind: config.PushStateChat, Title: m.Agent, Body: chatBody(m), At: n.now(), MessageID: m.ID, MessageKind: m.Kind}
	for _, o := range m.Options {
		a.Options = append(a.Options, Option(o))
	}
	n.enqueue(a)
}

// chatBody is a chat message's text, or its file names when it has none.
func chatBody(m chat.Message) string {
	if m.Text != "" {
		return m.Text
	}
	names := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		names = append(names, a.Filename)
	}
	return strings.Join(names, ", ")
}

// enqueue queues a without ever blocking the publisher.
func (n *Notifier) enqueue(a Alert) {
	select {
	case n.queue <- a:
	default: // queue full: shed the alert rather than block the publisher
	}
}

// observe records agent's state and queues an alert when it newly entered a
// needs-you state.
func (n *Notifier) observe(agent string, att observe.AgentAttention) {
	n.mu.Lock()
	prev, seen := n.last[agent]
	n.last[agent] = att.State
	if seen && prev == att.State {
		n.mu.Unlock()
		return
	}
	// A preview belongs to one turn: leaving finished drops any that arrived
	// late, and a finished transition consumes it whether or not it alerts.
	preview := att.Preview
	if att.State == observe.AttentionFinished {
		if preview == "" {
			preview = n.previews[agent]
		}
		delete(n.previews, agent)
	} else {
		delete(n.previews, agent)
	}
	var permissionID string
	// A classic agent's permission_prompt hook reports no reason; any other
	// reason (a question) is not the permission prompt.
	if att.State == observe.AttentionNeedsInput && (att.Reason == nil || att.Reason.Kind == observe.AttentionReasonPermission) {
		permissionID = n.openPermissions[agent]
	}
	n.mu.Unlock()
	title, ok := needsYouTitle(att)
	if !ok {
		return
	}
	if n.allow != nil && !n.allow(agent, string(att.State)) {
		return
	}
	a := Alert{Agent: agent, Kind: string(att.State), Title: title, Body: alertBody(att, preview), At: n.now()}
	if permissionID != "" {
		a.MessageID, a.MessageKind = permissionID, chat.KindPermission
	}
	n.enqueue(a)
}

// needsYouTitle is the human summary for a needs-you state; ok=false for
// every other state.
func needsYouTitle(att observe.AgentAttention) (string, bool) {
	switch att.State {
	case observe.AttentionNeedsInput:
		return needsInputTitle(att.Reason), true
	case observe.AttentionFinished:
		return "Finished", true
	case observe.AttentionErrored:
		return "Hit an error", true
	}
	return "", false
}

// alertBody is what needs attention, shown under the title: the reason's
// detail (question text, command, path) for needs_input and errored, and the
// turn's preview for finished. Empty means a title-only push.
func alertBody(att observe.AgentAttention, preview string) string {
	if att.State == observe.AttentionFinished {
		return preview
	}
	if att.Reason == nil {
		return ""
	}
	return att.Reason.Detail
}

func needsInputTitle(r *observe.AttentionReason) string {
	if r == nil {
		return "Waiting for your input"
	}
	var title string
	switch r.Kind {
	case observe.AttentionReasonPermission:
		title = "Needs permission"
	case observe.AttentionReasonQuestion:
		title = "Has a question"
	case observe.AttentionReasonElicitation:
		title = "Needs your input"
	default:
		return "Waiting for your input"
	}
	if r.Tool != "" {
		title += ": " + r.Tool
	}
	return title
}
