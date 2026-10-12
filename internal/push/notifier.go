package push

import (
	"context"
	"sync"
	"time"

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
	case *observe.AgentStoppedPayload:
		n.mu.Lock()
		delete(n.last, p.Agent)
		n.mu.Unlock()
	}
}

// observe records agent's state and queues an alert when it newly entered a
// needs-you state.
func (n *Notifier) observe(agent string, att observe.AgentAttention) {
	n.mu.Lock()
	prev, seen := n.last[agent]
	n.last[agent] = att.State
	n.mu.Unlock()
	if seen && prev == att.State {
		return
	}
	title, ok := needsYouTitle(att)
	if !ok {
		return
	}
	if n.allow != nil && !n.allow(agent, string(att.State)) {
		return
	}
	select {
	case n.queue <- Alert{Agent: agent, Kind: string(att.State), Title: title, At: n.now()}:
	default: // queue full: shed the alert rather than block the publisher
	}
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
