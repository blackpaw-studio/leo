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
	Version    int       `json:"version"`
	ServerTime time.Time `json:"server_time"`
	LeoVersion string    `json:"leo_version"`
	Agents     []Agent   `json:"agents"`
	Tasks      []Task    `json:"tasks"`
	RecentRuns []TaskRun `json:"recent_runs"`
}

// Status is an agent's lifecycle state, mirroring the supervisor's own vocabulary.
type Status string

const (
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusSuspended Status = "suspended"
	StatusStopped   Status = "stopped"
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

	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`

	CurrentAction *Action `json:"current_action"`
}

// ActionKind names the provenance of an Action's detail, so consumers can tell how much
// to trust it. Only ActionKindPane exists today; the field exists so a future structured
// source can be added without changing the shape.
//
// Consumers must treat an unrecognized kind as displayable: fall back to showing Detail
// as plain text rather than dropping the action, so adding a kind never blanks out an
// older consumer's display.
type ActionKind string

// ActionKindPane marks detail scraped from the agent's tmux pane.
const ActionKindPane ActionKind = "pane"

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

// MaxRecentRuns caps Snapshot.RecentRuns, newest first.
const MaxRecentRuns = 50

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
