package observe

import "time"

// EventType is the SSE event name. Consumers must ignore types they do not recognize, so
// new types can be added without breaking them.
type EventType string

const (
	// EventHello opens every stream, carrying the sequence number the stream starts from.
	EventHello EventType = "hello"
	// EventAgentSpawned announces an agent that did not exist before, with its full state.
	EventAgentSpawned EventType = "agent_spawned"
	// EventAgentStateChanged reports a lifecycle transition, including suspend and resume.
	EventAgentStateChanged EventType = "agent_state_changed"
	// EventAgentActivity reports a change in working/idle state or current action.
	EventAgentActivity EventType = "agent_activity"
	// EventAgentStopped announces an agent leaving supervision.
	EventAgentStopped EventType = "agent_stopped"
	// EventTaskRunStarted announces a task firing.
	EventTaskRunStarted EventType = "task_run_started"
	// EventTaskRunSucceeded announces a task firing that finished cleanly.
	EventTaskRunSucceeded EventType = "task_run_succeeded"
	// EventTaskRunFailed announces a task firing that errored, timed out, or was killed.
	EventTaskRunFailed EventType = "task_run_failed"
	// EventAgentMessage announces one agent-to-agent message being routed, as a pair of
	// names only. Never carries the message body.
	EventAgentMessage EventType = "agent_message"
)

// Meta is the sequence number and timestamp carried by every event payload. The bus
// stamps it at publish time; producers leave it zero.
//
// Seq is monotonic within one daemon lifetime and exists so a consumer can detect that it
// missed events. No history is retained, so the response to a gap is to refetch the
// snapshot, not to request a replay.
type Meta struct {
	Seq uint64    `json:"seq"`
	At  time.Time `json:"at"`
}

// Payload is one event's body. Implementations embed Meta, which supplies stamp.
//
// Because stamp has a pointer receiver, only the *pointer* to a payload satisfies this
// interface: publish &AgentActivityPayload{...}, never AgentActivityPayload{...}. That is
// deliberate — the bus stamps the payload in place, so it must not receive a copy.
type Payload interface {
	stamp(seq uint64, at time.Time)
}

func (m *Meta) stamp(seq uint64, at time.Time) {
	m.Seq = seq
	m.At = at
}

// Event pairs a payload with the SSE event name it is published under.
type Event struct {
	Type    EventType
	Payload Payload
}

// HelloPayload opens a stream so a consumer can tell whether the snapshot it already
// fetched predates the events it is about to receive.
type HelloPayload struct {
	Meta
	Version    int       `json:"version"`
	ServerTime time.Time `json:"server_time"`
}

// AgentSpawnedPayload carries the whole agent, since the consumer has never seen it.
type AgentSpawnedPayload struct {
	Meta
	Agent Agent `json:"agent"`
}

// AgentStateChangedPayload reports lifecycle movement. Resume arrives here as
// a new Status rather than as a distinct event type, so consumers branch on Status alone.
type AgentStateChangedPayload struct {
	Meta
	Agent    string `json:"agent"`
	Status   Status `json:"status"`
	Restarts int    `json:"restarts"`
	// WakeOnMessage mirrors Agent.WakeOnMessage: only meaningful when Status
	// is StatusStopped, always present, always set alongside Status via
	// AgentDormancy so the two can never disagree.
	WakeOnMessage bool `json:"wake_on_message"`
}

// AgentActivityPayload reports the tracker's latest reading for one agent.
type AgentActivityPayload struct {
	Meta
	Agent         string   `json:"agent"`
	Activity      Activity `json:"activity"`
	CurrentAction *Action  `json:"current_action"`
}

// AgentStoppedPayload announces an agent leaving supervision.
type AgentStoppedPayload struct {
	Meta
	Agent string `json:"agent"`
	// WakeOnMessage is true only when this stop is a dormancy transition an
	// inbound message may reverse (an idle sweep, or a manual stop with
	// wake-on-message requested); false for a transient kill ahead of an
	// immediate respawn (Reset, Restart, template switch) or a permanent
	// departure (Delete, rename). Always present, matching Agent.WakeOnMessage
	// and AgentStateChangedPayload.WakeOnMessage's never-disagree contract.
	WakeOnMessage bool `json:"wake_on_message"`
}

// TaskRunPayload carries a task firing. It serves the started, succeeded, and failed
// event types; the Run's own Status distinguishes them.
type TaskRunPayload struct {
	Meta
	Run TaskRun `json:"run"`
}

// AgentMessagePayload reports that one agent messaged another: the pair, and nothing
// else. There is deliberately no field for the message body, and none may be added — a
// consumer of this stream is told THAT two agents are talking, never what about.
//
// From is empty when the sender is not an agent (a human messaging from the web UI);
// leo does not invent a sender. Consumers wanting agent-to-agent activity should require
// both names.
//
// From is self-asserted by the calling agent and is not an authenticated identity. It is
// fine for display; it must not be used for authorization or attribution.
type AgentMessagePayload struct {
	Meta
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// Publisher is the seam producers publish through — the supervisor for agent events, the
// task runner for run events. Narrowing to this interface keeps producers from depending
// on the whole bus, and makes them trivial to test with a recording fake.
type Publisher interface {
	Publish(ev Event)
}
