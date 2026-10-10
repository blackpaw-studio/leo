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
	// EventFileSurfaced announces a file an agent pushed to the user's attention
	// (leo_surface_file). Its payload is FileSurfacedPayload.
	EventFileSurfaced EventType = "file_surfaced"
	// EventAgentTurnStarted announces a bridged agent beginning a turn.
	EventAgentTurnStarted EventType = "agent_turn_started"
	// EventAgentTurnCompleted announces a bridged agent's turn ending, with
	// a clamped preview of its final message and the turn's usage.
	EventAgentTurnCompleted EventType = "agent_turn_completed"
	// EventAgentSessionEnded announces a bridged agent's Claude session ending.
	EventAgentSessionEnded EventType = "agent_session_ended"
	// EventAgentCompaction reports one phase of a conversation compaction.
	EventAgentCompaction EventType = "agent_compaction"
	// EventAgentUsage reports a usage change outside a turn completion.
	EventAgentUsage EventType = "agent_usage"
	// EventDispatchChanged carries a dispatch's full record whenever it
	// changes; a terminal Status marks the end.
	EventDispatchChanged EventType = "dispatch_changed"
	// EventDispatchRemoved announces that a finished dispatch has left
	// Snapshot.Dispatches: its DispatchLinger expired (or its record is gone).
	EventDispatchRemoved EventType = "dispatch_removed"
)

// Features are the optional capabilities this daemon's API offers,
// advertised on the hello so a client can tell a field that is empty from
// one this daemon never sends.
const (
	FeatureBridgeTurns     = "bridge_turns"
	FeatureAttentionReason = "attention_reason"
	FeatureDispatchTree    = "dispatch_tree"
	FeatureAgentUsage      = "agent_usage"
	FeatureAgentControl    = "agent_control"
	FeatureDispatchAttach  = "dispatch_attach"
	FeatureDispatchRemoved = "dispatch_removed"
	FeatureStateSeq        = "state_seq"
	// FeatureAttachDispatchPlacement: `leo agent attach --dispatch-placement`
	// steers where dispatch viewers open for the attached client.
	FeatureAttachDispatchPlacement = "attach_dispatch_placement"
	// FeatureDispatchPlacementLive: live interactive dispatch viewers follow
	// their caller session's effective placement as clients attach and
	// detach, moving between the caller's session and leo-dispatch (the
	// `background` viewer_kind).
	FeatureDispatchPlacementLive = "dispatch_placement_live"
	// FeatureAgentEnvironments: agents carry environments /
	// environments_source / environment_error, agent_state_changed may carry
	// environments, and the environment routes exist (names only, never
	// values).
	FeatureAgentEnvironments = "agent_environments"
)

// Features returns the hello's features list, a fresh copy each call.
func Features() []string {
	return []string{FeatureBridgeTurns, FeatureAttentionReason, FeatureDispatchTree, FeatureAgentUsage, FeatureAgentControl, FeatureDispatchAttach, FeatureDispatchRemoved, FeatureStateSeq, FeatureAttachDispatchPlacement, FeatureDispatchPlacementLive, FeatureAgentEnvironments}
}

// ActivityMinInterval is the most often agent_activity is published per
// agent (the trailing edge always is), and the most often the mod sends an
// activity report.
const ActivityMinInterval = time.Second

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
	// BootID is random per daemon process. Attention revisions restart with
	// the daemon, so a consumer that sees BootID change must re-baseline.
	BootID string `json:"boot_id"`
	// Features lists the optional capabilities this daemon offers (see
	// Features). Consumers ignore names they do not recognise.
	Features []string `json:"features"`
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

// AgentEnvironmentsChangedPayload is the agent_state_changed an environments
// change (set-environment) publishes. It carries AgentStateChangedPayload's
// fields plus the agent's new environments, every one always present on the
// wire: environments is [] when the effective list is empty, and
// environment_error is an explicit null when the list resolves. Plain
// lifecycle state changes carry none of the three, so their absence means
// "unchanged", and their presence here means exactly what they say.
type AgentEnvironmentsChangedPayload struct {
	Meta
	Agent         string `json:"agent"`
	Status        Status `json:"status"`
	Restarts      int    `json:"restarts"`
	WakeOnMessage bool   `json:"wake_on_message"`
	// Environments is the new effective ordered names (never nil).
	Environments []string `json:"environments"`
	// EnvironmentsSource is "override" or "default".
	EnvironmentsSource string `json:"environments_source"`
	// EnvironmentError is non-nil when a name no longer resolves.
	EnvironmentError *string `json:"environment_error"`
}

// AgentActivityPayload reports the tracker's latest reading for one agent.
type AgentActivityPayload struct {
	Meta
	Agent         string   `json:"agent"`
	Activity      Activity `json:"activity"`
	CurrentAction *Action  `json:"current_action"`
	// Attention is absent when the agent has no attention source (e.g. a
	// harness without hook plumbing). See AttentionStore.
	Attention *AgentAttention `json:"attention,omitempty"`
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

// TurnOutcome is how a bridged agent's turn ended.
type TurnOutcome string

const (
	TurnCompleted TurnOutcome = "completed"
	TurnAborted   TurnOutcome = "aborted"
)

// TurnTokens is one turn's own token counts.
type TurnTokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

// AgentTurnStartedPayload announces a turn beginning. Never carries the prompt.
type AgentTurnStartedPayload struct {
	Meta
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

// AgentTurnCompletedPayload announces a turn ending. Preview is the final
// message clamped by ClampPreview: display text, never parse it.
type AgentTurnCompletedPayload struct {
	Meta
	Agent     string        `json:"agent"`
	SessionID string        `json:"session_id"`
	Outcome   TurnOutcome   `json:"outcome"`
	Preview   string        `json:"preview"`
	Tokens    TurnTokens    `json:"tokens"`
	CostUSD   *float64      `json:"cost_usd,omitempty"`
	Context   *ContextUsage `json:"context,omitempty"`
}

// AgentSessionEndedPayload announces a Claude session ending. Reason is the
// mod's, clamped by ClampDetail.
type AgentSessionEndedPayload struct {
	Meta
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

// CompactionPhase is one step of a compaction.
type CompactionPhase string

const (
	CompactionStarted   CompactionPhase = "started"
	CompactionCompleted CompactionPhase = "completed"
	CompactionFailed    CompactionPhase = "failed"
)

// CompactionTrigger says who asked for a compaction.
type CompactionTrigger string

const (
	CompactionManual CompactionTrigger = "manual"
	CompactionAuto   CompactionTrigger = "auto"
)

// AgentCompactionPayload reports one compaction phase. ContextPercent is the
// context window fill when known.
type AgentCompactionPayload struct {
	Meta
	Agent          string            `json:"agent"`
	Phase          CompactionPhase   `json:"phase"`
	Trigger        CompactionTrigger `json:"trigger"`
	ContextPercent *float64          `json:"context_percent,omitempty"`
}

// AgentUsagePayload reports an agent's usage when it changes outside a turn
// completion (which already carries it).
type AgentUsagePayload struct {
	Meta
	Agent string     `json:"agent"`
	Usage AgentUsage `json:"usage"`
}

// DispatchChangedPayload carries a dispatch's whole current record.
type DispatchChangedPayload struct {
	Meta
	Dispatch Dispatch `json:"dispatch"`
}

// DispatchRemovedPayload names a dispatch that has left Snapshot.Dispatches.
// Consumers drop it; a later dispatch_changed would have to re-add it.
type DispatchRemovedPayload struct {
	Meta
	ID string `json:"id"`
}

// Publisher is the seam producers publish through — the supervisor for agent events, the
// task runner for run events. Narrowing to this interface keeps producers from depending
// on the whole bus, and makes them trivial to test with a recording fake.
type Publisher interface {
	Publish(ev Event)
}
