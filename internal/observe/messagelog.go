package observe

import (
	"sync"
	"time"
)

const (
	// MaxRecentMessages bounds how many agent-to-agent pairs the snapshot
	// carries. A chatty pair must not grow /api/v1/state without limit.
	MaxRecentMessages = 50
	// RecentMessageWindow is how far back Recent looks. Consumers animating
	// "these two are talking" apply their own, tighter window on top; this
	// only has to outlast theirs.
	RecentMessageWindow = 10 * time.Minute
)

// MessageLog is a bounded, newest-last, in-memory record of agent-to-agent
// messages. Like RunLog it implements Publisher and wraps another Publisher,
// recording every AgentMessagePayload as it passes through and forwarding
// every event — message-related or not — unchanged.
//
// Also like RunLog, it is deliberately not a bus subscriber: the bus drops
// slow subscribers rather than blocking a publisher, and a log that could
// silently lose entries would make the snapshot's recent_messages
// untrustworthy. Wrapping the bus makes recording synchronous with publish.
//
// It stores pairs and timestamps only. AgentMessagePayload has no field for
// message content, so there is none to record.
type MessageLog struct {
	mu       sync.Mutex
	next     Publisher
	capacity int
	// messages is newest-LAST: consumers replay a conversation in the order
	// it happened. (RunLog is newest-first; runs are read as a status list,
	// these are read as a timeline.)
	messages []AgentMessage
}

// AgentMessage is one recorded pair, as served in the snapshot's
// recent_messages. Deliberately identical in shape to the event payload minus
// its Meta.
type AgentMessage struct {
	From string    `json:"from,omitempty"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// NewMessageLog creates a MessageLog wrapping next (nil is a valid
// "record only, no forwarding" configuration), bounded to capacity entries.
// capacity <= 0 uses MaxRecentMessages.
func NewMessageLog(next Publisher, capacity int) *MessageLog {
	if capacity <= 0 {
		capacity = MaxRecentMessages
	}
	return &MessageLog{next: next, capacity: capacity}
}

// Publish records ev if it carries an AgentMessagePayload, then forwards ev
// unchanged to the wrapped Publisher, if any.
//
// The recorded timestamp falls back to now when the payload has none. That is
// the normal case in production: the log sits IN FRONT of the bus, and the bus
// is what stamps Meta — so at record time the payload's At is still zero, and
// recording it verbatim would date every entry to the zero time and have the
// age filter discard it immediately. Recording here and stamping at the bus
// happen microseconds apart, so the snapshot's `at` and the event's `at` agree
// for any purpose this data serves.
func (l *MessageLog) Publish(ev Event) {
	if p, ok := ev.Payload.(*AgentMessagePayload); ok {
		at := p.At
		if at.IsZero() {
			at = time.Now()
		}
		l.record(AgentMessage{From: p.From, To: p.To, At: at})
	}
	if l.next != nil {
		l.next.Publish(ev)
	}
}

// record appends msg and trims the oldest entries past capacity.
func (l *MessageLog) record(msg AgentMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.messages = append(l.messages, msg)
	if len(l.messages) > l.capacity {
		l.messages = l.messages[len(l.messages)-l.capacity:]
	}
}

// Recent returns up to n messages no older than RecentMessageWindow before
// now, oldest first. n <= 0 means "all of them (within the window)"; when n
// is smaller than what's held, the NEWEST n are returned.
//
// Age is filtered on read rather than swept on a timer: nothing else needs a
// goroutine, and an entry that ages out between writes simply never appears.
func (l *MessageLog) Recent(n int, now time.Time) []AgentMessage {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-RecentMessageWindow)
	fresh := make([]AgentMessage, 0, len(l.messages))
	for _, m := range l.messages {
		if m.At.After(cutoff) {
			fresh = append(fresh, m)
		}
	}
	if n > 0 && n < len(fresh) {
		fresh = fresh[len(fresh)-n:]
	}
	return fresh
}
