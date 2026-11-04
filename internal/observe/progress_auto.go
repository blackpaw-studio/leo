package observe

import (
	"slices"
	"strings"
	"time"
)

// AutoTask is one daemon-originated report (see TaskSource): a long shell
// command or a dispatch run the daemon noticed. Auto tasks are indeterminate,
// never stale, and clear themselves AutoExpireAfter after they end.
type AutoTask struct {
	// ID must start with AutoIDPrefix.
	ID string
	// Title names the task; empty keeps the stored title (required on create).
	Title string
	// Status is the current phase ("waiting on you"); empty keeps the stored
	// one.
	Status string
	// State is running (the default) or a terminal state.
	State TaskState
	// StartedAt is the real start of the work, which can precede the report
	// by the 20 s a command must run before it is published. Zero means now.
	StartedAt time.Time
}

// UpsertAuto creates, updates or ends the auto task a.ID on agent and returns
// the stored task. A new running task is refused with ErrTaskCap past
// MaxTasksPerClass running auto tasks; the caller skips that entry and its
// later end. A running report on an ended task starts a fresh run. Ending an
// unknown task is ErrTaskNotFound; a repeated end is a no-op.
func (s *ProgressStore) UpsertAuto(agent string, a AutoTask) (AgentTask, error) {
	if s == nil {
		return AgentTask{}, errProgressUnavailable
	}
	a, err := a.normalize()
	if err != nil {
		return AgentTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	e := s.agents[agent][a.ID]

	switch {
	case e != nil && e.task.State.IsTerminal() && a.State.IsTerminal():
		return e.task.clone(), nil
	case e == nil && a.State.IsTerminal():
		return AgentTask{}, ErrTaskNotFound
	case e != nil && e.task.State == TaskRunning && a.State == TaskRunning && !a.changes(e.task):
		return e.task.clone(), nil
	}
	next, err := a.apply(e, now)
	if err != nil {
		return AgentTask{}, err
	}
	if next.State == TaskRunning && (e == nil || e.task.State != TaskRunning) &&
		s.countLocked(agent, classAutoRunning) >= MaxTasksPerClass {
		return AgentTask{}, ErrTaskCap
	}
	if e == nil {
		e = &taskEntry{agent: agent}
		if s.agents[agent] == nil {
			s.agents[agent] = make(map[string]*taskEntry)
		}
		s.agents[agent][a.ID] = e
	}
	e.task = next
	s.armLocked(e)
	if next.State.IsTerminal() {
		s.flushLocked(e)
	} else {
		s.publishLocked(e)
	}
	s.enforceBoundLocked(agent, e)
	return e.task.clone(), nil
}

// RemoveAutoRunning drops agent's running auto tasks whose id starts with
// prefix, publishing a null for each, and returns their ids (sorted).
func (s *ProgressStore) RemoveAutoRunning(agent, prefix string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for _, e := range s.entriesLocked(agent) {
		if isAuto(e.task) && isRunning(e.task) && strings.HasPrefix(e.task.ID, prefix) {
			removed = append(removed, e.task.ID)
			s.removeLocked(e)
		}
	}
	slices.Sort(removed)
	return removed
}

// normalize validates a and cleans its text. Unlike an agent report, an
// over-long title or status is cut, not rejected: the daemon wrote it.
func (a AutoTask) normalize() (AutoTask, error) {
	if !strings.HasPrefix(a.ID, AutoIDPrefix) || !taskIDPattern.MatchString(a.ID) {
		return AutoTask{}, invalidTask("auto task id must start with %q and be 1-%d characters of [a-z0-9._-]", AutoIDPrefix, MaxTaskIDLen)
	}
	if a.State == "" {
		a.State = TaskRunning
	}
	if !a.State.valid() {
		return AutoTask{}, invalidTask("state must be running, succeeded, failed or cancelled")
	}
	a.Title = cutTaskText(a.Title, MaxTaskTitleLen)
	a.Status = cutTaskText(a.Status, MaxTaskStatusLen)
	return a, nil
}

// cutTaskText strips control and format characters, trims, and cuts to limit
// runes.
func cutTaskText(s string, limit int) string {
	cleaned, err := cleanTaskText("text", strings.ToValidUTF8(s, ""), 1<<30)
	if err != nil {
		return ""
	}
	if r := []rune(cleaned); len(r) > limit {
		return strings.TrimSpace(string(r[:limit]))
	}
	return cleaned
}

// changes reports whether a running report alters the stored running task.
func (a AutoTask) changes(t AgentTask) bool {
	return (a.Title != "" && a.Title != t.Title) || (a.Status != "" && a.Status != t.Status)
}

// apply returns the task e becomes under a (e nil: a new task).
func (a AutoTask) apply(e *taskEntry, now time.Time) (AgentTask, error) {
	var t AgentTask
	switch {
	case e != nil && e.task.State == TaskRunning:
		t = e.task.clone()
	case e != nil: // fresh run of an ended task: only the title and revision carry over
		t = AgentTask{ID: a.ID, Source: SourceAuto, Mode: TaskIndeterminate, Title: e.task.Title, Revision: e.task.Revision}
	default:
		t = AgentTask{ID: a.ID, Source: SourceAuto, Mode: TaskIndeterminate}
	}
	if t.StartedAt.IsZero() {
		t.StartedAt = a.StartedAt
		if t.StartedAt.IsZero() || t.StartedAt.After(now) {
			t.StartedAt = now
		}
	}
	if a.Title != "" {
		t.Title = a.Title
	}
	if t.Title == "" {
		return AgentTask{}, invalidTask("title is required when creating a task")
	}
	if a.Status != "" {
		t.Status = a.Status
	}
	t.State = a.State
	if t.State.IsTerminal() {
		ended := now
		t.EndedAt = &ended
	}
	t.UpdatedAt = now
	t.Revision++
	return t, nil
}
