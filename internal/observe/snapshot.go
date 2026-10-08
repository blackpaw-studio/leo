// Package observe exposes read-only, live views of the Leo fleet: a point-in-time
// snapshot of agents and tasks, plus a stream of events describing changes to it.
//
// The package is deliberately consumer-agnostic. It answers "what is running and what is
// it doing", and nothing about how any particular consumer wants to display that. See
// docs/specs/2026-07-31-observability-api.md for the wire contract these types serialize
// to; the JSON tags here are that contract and must evolve additively.
package observe

import "time"

// SnapshotVersion is the contract version carried in every snapshot. Consumers use it to
// reject a payload shaped by an incompatible future Leo. Additive field changes keep the
// same version; removals or renames require a new one.
const SnapshotVersion = 1

// Snapshot is the whole observable world at one instant, served by GET /api/v1/state.
type Snapshot struct {
	// Meta orders the snapshot against the event stream: it reflects every
	// event with seq <= Meta.Seq (and may already reflect later ones), so a
	// client applies only events whose seq is greater. The bus has no replay,
	// so a client subscribes to the stream first and fetches the snapshot
	// second (refetching if hello.seq is greater than Meta.Seq). Advertised by
	// the state_seq feature.
	Meta       SnapshotMeta `json:"meta"`
	Version    int          `json:"version"`
	ServerTime time.Time    `json:"server_time"`
	LeoVersion string       `json:"leo_version"`
	Agents     []Agent      `json:"agents"`
	Tasks      []Task       `json:"tasks"`
	RecentRuns []TaskRun    `json:"recent_runs"`
	// RecentMessages are recent agent-to-agent message pairs (names and
	// timestamps only, never content), so a consumer connecting mid-
	// conversation can seed what it missed rather than waiting for the next
	// message. Bounded by MaxRecentMessages and RecentMessageWindow.
	RecentMessages []AgentMessage `json:"recent_messages"`
	// Dispatches are live leo dispatches plus those ended within
	// DispatchLinger, flat: clients build the tree from ParentDispatchID.
	// Always present: empty when there are none or no dispatch source.
	Dispatches []Dispatch `json:"dispatches"`
}

// SnapshotMeta is the snapshot's place in the event stream.
type SnapshotMeta struct {
	Seq uint64 `json:"seq"`
}

// Status is an agent's lifecycle state, mirroring the supervisor's own vocabulary.
type Status string

const (
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusStopped  Status = "stopped"
)

// Activity is an agent's live work state, derived from tmux session activity. It is
// orthogonal to Status: a running agent is frequently idle.
type Activity string

const (
	// ActivityWorking means the agent's tmux session produced output or took input
	// within the idle threshold.
	ActivityWorking Activity = "working"
	// ActivityIdle means the session has been quiet for longer than the idle threshold.
	ActivityIdle Activity = "idle"
	// ActivityUnknown means there is nothing to measure — no tmux session, or the agent
	// is not running.
	ActivityUnknown Activity = "unknown"
)

// Agent is one supervised agent as an observer sees it.
type Agent struct {
	Name      string `json:"name"`
	Template  string `json:"template,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Model     string `json:"model,omitempty"`
	Harness   string `json:"harness,omitempty"`

	Status   Status   `json:"status"`
	Activity Activity `json:"activity"`
	Restarts int      `json:"restarts"`
	// WakeOnMessage is only meaningful when Status is StatusStopped: true
	// means the agent was idle-swept and an inbound message auto-wakes it;
	// false means either it is not dormant at all, or it was dormant by a
	// manual stop and stays that way until an operator restarts it
	// explicitly. Always present (no omitempty) so a consumer can read the
	// manually-stopped case as an explicit false rather than an absent
	// field. Set only via AgentDormancy, which enforces this pairing —
	// never assign Status and WakeOnMessage independently.
	WakeOnMessage bool `json:"wake_on_message"`

	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`

	CurrentAction *Action `json:"current_action"`

	// Attention is absent when the agent has no attention source.
	Attention *AgentAttention `json:"attention,omitempty"`

	// SurfacedFiles are the files this incarnation pushed to the user's
	// attention (leo_surface_file), oldest first, capped at
	// MaxSurfacedFiles. Absent when there are none.
	SurfacedFiles []SurfacedFile `json:"surfaced_files,omitempty"`

	// Usage is the bridged agent's token and cost totals; absent without a
	// bridge feed reading for it.
	Usage *AgentUsage `json:"usage,omitempty"`
	// Outstanding counts the children still running for this agent; absent
	// when nothing reports any.
	Outstanding *Outstanding `json:"outstanding,omitempty"`
	// Bridge is the claude mod bridge's link state; empty (omitted) for
	// non-claude harnesses.
	Bridge BridgeState `json:"bridge,omitempty"`
}

// BridgeState is whether a claude agent's mod bridge is connected.
type BridgeState string

const (
	BridgeConnected BridgeState = "connected"
	BridgeAbsent    BridgeState = "absent"
)

// Outstanding counts an agent's still-running children: leo dispatches it
// called and native background subagents. While either is non-zero after a
// turn completes, the agent's attention stays working (B-051).
type Outstanding struct {
	Dispatches int `json:"dispatches"`
	Subagents  int `json:"subagents"`
}

// UsageTotals is accumulated tokens and cost over some span.
type UsageTotals struct {
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// ContextUsage is how full the model's context window is.
type ContextUsage struct {
	Tokens  int64   `json:"tokens"`
	Window  int64   `json:"window"`
	Percent float64 `json:"percent"`
}

// AgentUsage is a bridged agent's usage. Session resets when SessionID
// changes; Incarnation resets when the agent respawns.
type AgentUsage struct {
	SessionID   string        `json:"session_id"`
	Session     UsageTotals   `json:"session"`
	Incarnation UsageTotals   `json:"incarnation"`
	Context     *ContextUsage `json:"context,omitempty"`
}

// DispatchLinger is how long a finished dispatch stays in
// Snapshot.Dispatches after it ends.
const DispatchLinger = 60 * time.Second

// Dispatch is one leo subagent dispatch. Status uses the dispatch store's
// own vocabulary; ParentDispatchID is set when the caller is itself a
// dispatch.
type Dispatch struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Role           string `json:"role,omitempty"`
	Template       string `json:"template,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	ObservedEffort string `json:"observed_effort,omitempty"`
	Status         string `json:"status"`
	Stalled        bool   `json:"stalled"`
	// Pending summarizes the background work a waiting dispatch is paused
	// on, e.g. "1 shell · 1 monitor"; empty otherwise.
	Pending          string     `json:"pending,omitempty"`
	CallerAgent      string     `json:"caller_agent,omitempty"`
	ParentDispatchID string     `json:"parent_dispatch_id,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	// Attachable is true while `leo dispatch attach <id>` can show the
	// dispatch: an interactive dispatch whose TUI is alone in its own tmux
	// window. TmuxTarget is its tmux pane id (%N) on Leo's tmux server,
	// reported whenever the dispatch has a pane, attachable or not.
	Attachable bool    `json:"attachable"`
	TmuxTarget string  `json:"tmux_target,omitempty"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
}

// ActionKind names the provenance of an Action's detail, so consumers can tell how much
// to trust it. Only ActionKindPane exists today; the field exists so a future structured
// source can be added without changing the shape.
//
// Consumers must treat an unrecognized kind as displayable: fall back to showing Detail
// as plain text rather than dropping the action, so adding a kind never blanks out an
// older consumer's display.
type ActionKind string

const (
	// ActionKindPane marks detail scraped from the agent's tmux pane.
	ActionKindPane ActionKind = "pane"
	// ActionKindTool marks detail from the claude mod bridge: the running
	// tool's one-field summary, clamped by ClampDetail.
	ActionKindTool ActionKind = "tool"
)

// Action is a best-effort hint at what an agent is doing right now.
//
// Detail is whatever the harness happened to be rendering, sanitized and truncated. It is
// display text for humans: never parse it, never branch on it, and always escape it
// before rendering — it originates from arbitrary program output.
type Action struct {
	Kind   ActionKind `json:"kind"`
	Detail string     `json:"detail"`
}

// MaxActionDetail is the character budget for Action.Detail after sanitizing.
const MaxActionDetail = 120

// MaxTurnPreview is the character budget for a turn-completed preview.
const MaxTurnPreview = 280

// Task is a configured scheduled task.
type Task struct {
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule,omitempty"`
	Timezone  string     `json:"timezone,omitempty"`
	Enabled   bool       `json:"enabled"`
	Runtime   string     `json:"runtime,omitempty"`
	Template  string     `json:"template,omitempty"`
	Workspace string     `json:"workspace,omitempty"`
	Model     string     `json:"model,omitempty"`
	Harness   string     `json:"harness,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

// RunStatus is the outcome of a single task firing.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// TaskRun is one firing of a task.
//
// Workspace, Model, and Harness are denormalized onto the run rather than left as a join
// through Snapshot.Tasks: the producer already holds the values it resolved for this
// firing, and the join is not reliably available to a consumer — a task can be renamed or
// deleted while a run is in flight, and a task_run_* event can arrive before the consumer
// has ever fetched a snapshot.
type TaskRun struct {
	ID         string     `json:"id"`
	Task       string     `json:"task"`
	Status     RunStatus  `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`
	Error      string     `json:"error,omitempty"`
	Workspace  string     `json:"workspace,omitempty"`
	Model      string     `json:"model,omitempty"`
	Harness    string     `json:"harness,omitempty"`
}

// ClampRunFields truncates run's untrusted string fields (ID, Task,
// Workspace, Model, Harness, Error) to their respective Max*Len caps,
// returning a copy. It is the daemon's POST /observe/task-run handler's
// only line of defense against a producer that sends a field far larger
// than any legitimate value could be — see the Max* constants' doc comment.
func ClampRunFields(run TaskRun) TaskRun {
	run.ID = truncateRunes(run.ID, MaxObserveRunIDLen)
	run.Task = truncateRunes(run.Task, MaxObserveRunTaskLen)
	run.Workspace = truncateRunes(run.Workspace, MaxObserveRunWorkspaceLen)
	run.Model = truncateRunes(run.Model, MaxObserveRunModelLen)
	run.Harness = truncateRunes(run.Harness, MaxObserveRunHarnessLen)
	run.Error = truncateRunes(run.Error, MaxObserveRunErrorLen)
	return run
}

// MaxRecentRuns caps Snapshot.RecentRuns, newest first.
const MaxRecentRuns = 50

// Field length caps enforced by the daemon's POST /observe/task-run handler
// before a task-run event from a `leo run` subprocess is recorded and
// rebroadcast. Unlike every other field in this package, these values
// originate from an external process over IPC rather than from Leo's own
// in-process producers, so they get the same untrusted-input discipline
// Action.Detail already has (see MaxActionDetail): a misbehaving or
// malicious producer must not be able to inflate what RunLog retains or what
// every SSE subscriber receives.
const (
	MaxObserveRunIDLen        = 200
	MaxObserveRunTaskLen      = 200
	MaxObserveRunWorkspaceLen = 1024
	MaxObserveRunModelLen     = 200
	MaxObserveRunHarnessLen   = 200
	MaxObserveRunErrorLen     = 4096
)

// AgentActivity is the tracker's per-agent reading, keyed by agent name.
type AgentActivity struct {
	Activity       Activity
	LastActivityAt time.Time
	CurrentAction  *Action
}

// ActivityProvider is the seam between the activity tracker, which owns the sampling
// loop, and the HTTP layer, which only reads. Implementations must be safe for concurrent
// use and must return a copy the caller may retain.
type ActivityProvider interface {
	// Activities returns the latest reading for every agent the tracker knows about.
	// Agents absent from the map have no measurement; treat them as ActivityUnknown.
	Activities() map[string]AgentActivity
}

// BridgeAgentState is the bridge feed's projection for one agent, merged
// into its Agent row. Zero fields mean the feed has no reading for them.
type BridgeAgentState struct {
	Bridge    BridgeState
	Usage     *AgentUsage
	Subagents int
	// Reason is set while the agent is blocked on the user.
	Reason *AttentionReason
	// CurrentAction is the main loop's running tool (kind "tool"); nil
	// between tool calls.
	CurrentAction *Action
}

// DispatchSnapshotter is the attention store's seam onto outstanding
// dispatch counts, keyed by each caller's bridge key (never its mutable
// name). Each snapshot carries a generation that increases in the order the
// snapshots read the dispatch store, so a consumer can refuse one older
// than what it already applied. Implementations must be safe for
// concurrent use.
type DispatchSnapshotter interface {
	DispatchSnapshot() (gen uint64, counts map[string]int)
}

// DispatchCounter is the seam the bridge feed and the snapshot read
// outstanding leo dispatches through (the dispatch store provides it).
// Implementations must be safe for concurrent use.
type DispatchCounter interface {
	// OutstandingDispatches returns each caller agent's count of non-terminal dispatches, keyed by agent name.
	OutstandingDispatches() map[string]int
}
