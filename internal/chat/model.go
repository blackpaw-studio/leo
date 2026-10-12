// Package chat is Leo Chat's daemon half: one message thread per agent,
// persisted in SQLite, with inbound delivery into the agent, outbound
// auto-capture of the agent's reply, agent-side tools (post, ask, history),
// permission prompts answerable from chat, and voice-note transcription.
// See docs/configuration/chat.md for the HTTP contract.
package chat

import (
	"encoding/json"
	"time"
)

// Message directions.
const (
	DirectionIn  = "in"  // the user to the agent
	DirectionOut = "out" // the agent (or a task, or the daemon) to the user
)

// Message kinds.
const (
	KindText       = "text"
	KindVoice      = "voice"
	KindFile       = "file"
	KindTask       = "task"
	KindAsk        = "ask"
	KindPermission = "permission"
	KindSystem     = "system"
)

// Message states. Inbound messages move pending → delivered | failed;
// ask and permission messages move open → answered | expired.
const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateFailed    = "failed"
	StateOpen      = "open"
	StateAnswered  = "answered"
	StateExpired   = "expired"
)

// AnswerTerminal is the answer recorded when a permission prompt was
// settled in the agent's own terminal rather than from chat.
const AnswerTerminal = "terminal"

// Option is one choice of an ask message.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Permission describes the tool call a permission message asks about.
type Permission struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	// input is the raw tool input, kept to match the PostToolUse that
	// settles the prompt in the terminal; it is not sent to clients.
	input json.RawMessage
}

// Message is one chat message.
type Message struct {
	ID          string       `json:"id"`
	Agent       string       `json:"agent"`
	Direction   string       `json:"direction"`
	Kind        string       `json:"kind"`
	Text        string       `json:"text"`
	Transcript  string       `json:"transcript,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Task        string       `json:"task,omitempty"`
	RunID       string       `json:"run_id,omitempty"`
	State       string       `json:"state"`
	Answer      string       `json:"answer,omitempty"`
	Options     []Option     `json:"options,omitempty"`
	Permission  *Permission  `json:"permission,omitempty"`
	Attachments []Attachment `json:"attachments"`
	TS          time.Time    `json:"ts"`
	// DeliveryRef names an inbound message's delivery into its agent: the
	// bridge command id it is delivered under, stored before delivery so a
	// restarted daemon can ask the agent's outbox whether it still holds it.
	DeliveryRef string `json:"-"`
}

// MarshalJSON writes the wire shape: state always, and attachments as []
// rather than null when there are none.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire Message // no methods: avoids recursion
	if m.Attachments == nil {
		m.Attachments = []Attachment{}
	}
	return json.Marshal(wire(m))
}

// Attachment is a file stored with a message. Path is the file on the
// daemon host; it is cleared when retention prunes the file.
type Attachment struct {
	ID        string    `json:"id"`
	MessageID string    `json:"message_id,omitempty"`
	Agent     string    `json:"-"`
	Filename  string    `json:"filename"`
	MIME      string    `json:"mime"`
	Bytes     int64     `json:"bytes"`
	Path      string    `json:"-"`
	Available bool      `json:"available"`
	Created   time.Time `json:"-"`
}

// PageResult is one page of a thread, newest first; HasMore says an older
// page exists.
type PageResult struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"has_more"`
}

// Thread is one agent's conversation summary.
type Thread struct {
	Agent       string  `json:"agent"`
	LastMessage Message `json:"last_message"`
	Unread      int     `json:"unread"`
	Pinned      bool    `json:"pinned"`
}

// Capture is a pending auto-capture: the delivered prompt of an inbound
// message whose reply the next Stop hook should post.
type Capture struct {
	MessageID   string
	Agent       string
	Prompt      string
	DeliveredAt time.Time
}

// DefaultAgent receives task output whose owner cannot be resolved, and is
// the pinned thread.
const DefaultAgent = "assistant"
