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

// AgentStateChangedPayload reports lifecycle movement. Suspend and resume arrive here as
// a new Status rather than as distinct event types, so consumers branch on Status alone.
type AgentStateChangedPayload struct {
	Meta
	Agent    string `json:"agent"`
	Status   Status `json:"status"`
	Restarts int    `json:"restarts"`
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
}

// TaskRunPayload carries a task firing. It serves the started, succeeded, and failed
// event types; the Run's own Status distinguishes them.
type TaskRunPayload struct {
	Meta
	Run TaskRun `json:"run"`
}

// Publisher is the seam producers publish through — the supervisor for agent events, the
// task runner for run events. Narrowing to this interface keeps producers from depending
// on the whole bus, and makes them trivial to test with a recording fake.
type Publisher interface {
	Publish(ev Event)
}
