package observe

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Agent-advertised task progress (FeatureAgentProgress). An agent reports
// long-running work (a build, a deploy) as tasks keyed by an agent-chosen id;
// the daemon validates, stamps and bounds them, and publishes changes.
// "Task" here never means a cron task (Snapshot.Tasks): the wire and Go names
// carry the Agent prefix wherever they could be confused.

// Field limits and per-agent bounds.
const (
	MaxTaskIDLen     = 64
	MaxTaskTitleLen  = 80
	MaxTaskStatusLen = 120
	// MaxTaskETA bounds an agent-supplied eta_seconds.
	MaxTaskETA = 30 * 24 * time.Hour
	// MaxTasksPerClass bounds each class of one agent's tasks independently:
	// running tasks (a new one is rejected with ErrTaskCap), failed tasks and
	// ended succeeded/cancelled tasks (the oldest is dropped).
	MaxTasksPerClass = 8
)

// TaskMode is how a task's progress is shown.
type TaskMode string

const (
	TaskDeterminate   TaskMode = "determinate"
	TaskIndeterminate TaskMode = "indeterminate"
)

// TaskState is where a task is in its life.
type TaskState string

const (
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
	TaskCancelled TaskState = "cancelled"
)

// IsTerminal reports whether the state ends a task.
func (s TaskState) IsTerminal() bool {
	return s == TaskSucceeded || s == TaskFailed || s == TaskCancelled
}

func (s TaskState) valid() bool { return s == TaskRunning || s.IsTerminal() }

// Task errors. ErrTaskInvalid maps to HTTP 400, ErrTaskCap to 409,
// ErrTaskNotFound to 404 and ErrTaskNotFailed to 409.
var (
	ErrTaskInvalid   = errors.New("invalid task update")
	ErrTaskCap       = fmt.Errorf("too many running tasks (max %d); finish one first", MaxTasksPerClass)
	ErrTaskNotFound  = errors.New("task not found")
	ErrTaskNotFailed = errors.New("only failed tasks can be dismissed")
	// ErrTaskUnauthorized: the caller's authority lapsed (its agent stopped)
	// before the write applied. Maps to HTTP 403.
	ErrTaskUnauthorized = errors.New("agent token is no longer valid")
)

// AgentTask is the wire shape of one advertised task. Optional values are
// omitted when unset; EndedAt is always present (null while running).
type AgentTask struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Mode      TaskMode   `json:"mode"`
	Percent   *float64   `json:"percent,omitempty"`
	Status    string     `json:"status,omitempty"`
	EtaAt     *time.Time `json:"eta_at,omitempty"`
	State     TaskState  `json:"state"`
	Stale     bool       `json:"stale"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at"`
	// Revision is per task and strictly increasing until the task is removed.
	Revision uint64 `json:"revision"`
}

// TaskUpdate is one agent report: an upsert where omitted fields keep their
// value. It is also the request body of POST /api/agent/progress (minus the
// token). Run it through Normalize before use.
type TaskUpdate struct {
	ID            string    `json:"id"`
	Title         *string   `json:"title,omitempty"`
	Percent       *float64  `json:"percent,omitempty"`
	Indeterminate *bool     `json:"indeterminate,omitempty"`
	Status        *string   `json:"status,omitempty"`
	ETASeconds    *float64  `json:"eta_seconds,omitempty"`
	State         TaskState `json:"state,omitempty"`
}

var taskIDPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

// Normalize validates the update and returns a cleaned copy: strings are
// stripped of control and format characters and trimmed, percent is clamped
// to 0–100. The receiver is never modified.
func (u TaskUpdate) Normalize() (TaskUpdate, error) {
	if !taskIDPattern.MatchString(u.ID) {
		return TaskUpdate{}, invalidTask("id must be 1-%d characters of [a-z0-9._-]", MaxTaskIDLen)
	}
	if u.State != "" && !u.State.valid() {
		return TaskUpdate{}, invalidTask("state must be running, succeeded, failed or cancelled")
	}
	if u.Percent != nil && u.Indeterminate != nil && *u.Indeterminate {
		return TaskUpdate{}, invalidTask("percent and indeterminate are mutually exclusive")
	}
	out := u
	var err error
	if u.Title != nil {
		var t string
		if t, err = cleanTaskText("title", *u.Title, MaxTaskTitleLen); err != nil {
			return TaskUpdate{}, err
		}
		if t == "" {
			return TaskUpdate{}, invalidTask("title must not be empty")
		}
		out.Title = &t
	}
	if u.Status != nil {
		var s string
		if s, err = cleanTaskText("status", *u.Status, MaxTaskStatusLen); err != nil {
			return TaskUpdate{}, err
		}
		out.Status = &s
	}
	if u.Percent != nil {
		if math.IsNaN(*u.Percent) || math.IsInf(*u.Percent, 0) {
			return TaskUpdate{}, invalidTask("percent must be a finite number")
		}
		p := math.Min(100, math.Max(0, *u.Percent))
		out.Percent = &p
	}
	if u.ETASeconds != nil {
		e := *u.ETASeconds
		if math.IsNaN(e) || e < 0 || e > MaxTaskETA.Seconds() {
			return TaskUpdate{}, invalidTask("eta_seconds must be between 0 and %d", int(MaxTaskETA.Seconds()))
		}
		out.ETASeconds = &e
	}
	return out, nil
}

func invalidTask(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTaskInvalid, fmt.Sprintf(format, args...))
}

// cleanTaskText strips control (Cc) and format (Cf, e.g. zero-width and
// bidi overrides) characters, trims, and enforces a rune limit.
func cleanTaskText(field, s string, limit int) (string, error) {
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
	if !utf8.ValidString(cleaned) {
		return "", invalidTask("%s is not valid UTF-8", field)
	}
	if utf8.RuneCountInString(cleaned) > limit {
		return "", invalidTask("%s must be at most %d characters", field, limit)
	}
	return cleaned, nil
}
