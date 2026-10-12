package chat

import "github.com/blackpaw-studio/leo/internal/observe"

// Event types published on the observability bus (/api/v1/events and the
// socket's /events).
const (
	// EventMessage announces a new message in any thread.
	EventMessage observe.EventType = "chat.message"
	// EventUpdated announces a change to an existing message: transcript
	// ready, state change, answer.
	EventUpdated observe.EventType = "chat.updated"
)

// Change names what a chat.updated event changed.
const (
	ChangeTranscript = "transcript"
	ChangeState      = "state"
	ChangeAnswer     = "answer"
)

// MessagePayload is the chat.message event.
type MessagePayload struct {
	observe.Meta
	Agent   string  `json:"agent"`
	Message Message `json:"message"`
}

// UpdatedPayload is the chat.updated event: the message as it now is.
type UpdatedPayload struct {
	observe.Meta
	Agent   string  `json:"agent"`
	Message Message `json:"message"`
	Change  string  `json:"change"`
}

func messageEvent(m Message) *MessagePayload { return &MessagePayload{Agent: m.Agent, Message: m} }

func updatedEvent(m Message, change string) *UpdatedPayload {
	return &UpdatedPayload{Agent: m.Agent, Message: m, Change: change}
}
