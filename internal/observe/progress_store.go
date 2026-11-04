package observe

import (
	"errors"
	"slices"
	"sync"
	"time"
)

// ProgressStore holds every agent's advertised tasks in memory (a daemon
// restart clears them). Each write bumps the task's revision and publishes
// agent_task_changed; removals publish the same event with a null task.
// All methods are nil-safe: an unwired store has no tasks and refuses
// updates.
type ProgressStore struct {
	mu        sync.Mutex
	clock     FeedClock
	publisher Publisher
	agents    map[string]map[string]*taskEntry
}

// taskEntry is one stored task. task is replaced wholesale on every write
// and never mutated through its pointers, so copies handed out share no
// writable state.
type taskEntry struct {
	agent string
	task  AgentTask
	// gen invalidates timers armed for an earlier state of the entry;
	// stopTimer cancels the one armed now.
	gen       uint64
	stopTimer func() bool

	// Publish coalescing: lastPublish is when the task last went out;
	// pending marks a held-back write whose trailing publish is armed
	// (stopFlush cancels it, flushGen voids one already firing).
	lastPublish time.Time
	pending     bool
	stopFlush   func() bool
	flushGen    uint64
}

// NewProgressStore creates an empty store. clock nil uses the wall clock;
// publisher may be nil.
func NewProgressStore(clock FeedClock, publisher Publisher) *ProgressStore {
	if clock == nil {
		clock = realFeedClock{}
	}
	return &ProgressStore{
		clock:     clock,
		publisher: publisher,
		agents:    make(map[string]map[string]*taskEntry),
	}
}

// Lifecycle timings.
const (
	// StaleAfter is how long a running task may go without an update before
	// it is flagged stale.
	StaleAfter = 15 * time.Minute
	// TaskPublishInterval is the minimum gap between one task's publishes
	// (at most two per second); terminal states and removals ignore it.
	TaskPublishInterval = 500 * time.Millisecond
	// ExpireAfter is how long a succeeded or cancelled task lingers.
	ExpireAfter = 10 * time.Minute
	// AutoExpireAfter is how long an ended auto task lingers, whatever its
	// end state: commands fail routinely, so a failure never persists.
	AutoExpireAfter = 60 * time.Second
)

var errProgressUnavailable = errors.New("task progress is not available")

// Upsert applies one agent report to agent's task u.ID and returns the
// resulting task. Omitted fields keep their value; an update to an ended
// task starts a fresh run that keeps only the title. The caller resolves
// agent from the authenticated token, never from the report.
func (s *ProgressStore) Upsert(agent string, raw TaskUpdate) (AgentTask, error) {
	return s.UpsertGuarded(agent, raw, nil)
}

// UpsertGuarded is Upsert that first re-checks, under the store's lock,
// that the caller may still write (valid non-nil): a stop that revokes the
// caller's token and then calls RemoveAgent can then never be followed by a
// late write that resurrects the agent's tasks. valid runs with the store
// locked, so it must not call back into the store.
func (s *ProgressStore) UpsertGuarded(agent string, raw TaskUpdate, valid func() bool) (AgentTask, error) {
	if s == nil {
		return AgentTask{}, errProgressUnavailable
	}
	u, err := raw.Normalize()
	if err != nil {
		return AgentTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if valid != nil && !valid() {
		return AgentTask{}, ErrTaskUnauthorized
	}
	now := s.clock.Now()
	e := s.agents[agent][u.ID]

	if e != nil && e.task.State.IsTerminal() && isRepeatEnd(e.task, u) {
		return e.task.clone(), nil
	}
	base, err := baseTask(e, u, now)
	if err != nil {
		return AgentTask{}, err
	}
	next := applyUpdate(base, u, now)
	if next.State == TaskRunning && (e == nil || e.task.State != TaskRunning) &&
		s.countLocked(agent, classAgentRunning) >= MaxTasksPerClass {
		return AgentTask{}, ErrTaskCap
	}
	if e == nil {
		e = &taskEntry{agent: agent}
		if s.agents[agent] == nil {
			s.agents[agent] = make(map[string]*taskEntry)
		}
		s.agents[agent][u.ID] = e
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

// Tasks returns agent's tasks oldest run first (nil when none), as copies.
func (s *ProgressStore) Tasks(agent string) []AgentTask {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasksLocked(agent)
}

// HasRunningAgentTask reports whether agent has a task it reported itself
// (not an auto one) that is still running.
func (s *ProgressStore) HasRunningAgentTask(agent string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.entriesLocked(agent), func(e *taskEntry) bool {
		return !isAuto(e.task) && isRunning(e.task)
	})
}

// AllTasks returns every agent's tasks keyed by agent name (agents without
// tasks are absent), as copies.
func (s *ProgressStore) AllTasks() map[string][]AgentTask {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]AgentTask, len(s.agents))
	for agent := range s.agents {
		out[agent] = s.tasksLocked(agent)
	}
	return out
}

// Dismiss removes one ended (succeeded, failed or cancelled) task.
// ErrTaskNotFound for an unknown task, ErrTaskRunning for a running one.
func (s *ProgressStore) Dismiss(agent, id string) error {
	if s == nil {
		return ErrTaskNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.agents[agent][id]
	if e == nil {
		return ErrTaskNotFound
	}
	if isAuto(e.task) {
		return ErrTaskNotDismissable
	}
	if !e.task.State.IsTerminal() {
		return ErrTaskRunning
	}
	s.removeLocked(e)
	return nil
}

// RemoveAgent drops all of agent's tasks, publishing a null for each.
func (s *ProgressStore) RemoveAgent(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entriesLocked(agent) {
		s.removeLocked(e)
	}
}

// RemoveNonFailed drops agent's running, succeeded and cancelled tasks, and every auto task,
// publishing a null for each. Failed tasks stay until dismissed (or the
// agent is deleted), so a failure outlives a stop, suspend or crash-restart.
func (s *ProgressStore) RemoveNonFailed(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entriesLocked(agent) {
		if e.task.State != TaskFailed || isAuto(e.task) {
			s.removeLocked(e)
		}
	}
}

// Move re-keys oldName's tasks to newName after a rename, publishing a null
// under the old name and the task under the new one.
func (s *ProgressStore) Move(oldName, newName string) {
	if s == nil || oldName == newName {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	moved := s.entriesLocked(oldName)
	if len(moved) == 0 {
		return
	}
	for _, e := range s.entriesLocked(newName) {
		s.removeLocked(e)
	}
	for _, e := range moved {
		s.removeLocked(e)
		e.agent = newName
		if s.agents[newName] == nil {
			s.agents[newName] = make(map[string]*taskEntry)
		}
		s.agents[newName][e.task.ID] = e
		s.armLocked(e)
		s.flushLocked(e)
	}
}

// isRepeatEnd reports an update that only restates an ended task's state.
func isRepeatEnd(t AgentTask, u TaskUpdate) bool {
	return u.State == t.State && u.Title == nil && u.Percent == nil &&
		u.Indeterminate == nil && u.Status == nil && u.ETASeconds == nil
}

// baseTask is the starting point an update applies to: the running task
// itself, or a fresh run (keeping only the title of an ended one).
func baseTask(e *taskEntry, u TaskUpdate, now time.Time) (AgentTask, error) {
	if e != nil && e.task.State == TaskRunning {
		return e.task.clone(), nil
	}
	base := AgentTask{ID: u.ID, Source: SourceAgent, Mode: TaskIndeterminate, State: TaskRunning, StartedAt: now}
	if e != nil {
		base.Title = e.task.Title
		base.Revision = e.task.Revision
	}
	if u.Title == nil && base.Title == "" {
		return AgentTask{}, invalidTask("title is required when creating a task")
	}
	return base, nil
}

// applyUpdate returns base with u's present fields applied and the
// daemon-stamped fields refreshed.
func applyUpdate(base AgentTask, u TaskUpdate, now time.Time) AgentTask {
	t := base
	if u.Title != nil {
		t.Title = *u.Title
	}
	switch {
	case u.Percent != nil:
		p := *u.Percent
		t.Mode, t.Percent = TaskDeterminate, &p
	case u.Indeterminate != nil && *u.Indeterminate:
		t.Mode, t.Percent = TaskIndeterminate, nil
	case u.Indeterminate != nil && t.Mode != TaskDeterminate:
		zero := 0.0
		t.Mode, t.Percent = TaskDeterminate, &zero
	}
	if u.Status != nil {
		t.Status = *u.Status
	}
	if u.ETASeconds != nil {
		eta := now.Add(time.Duration(*u.ETASeconds * float64(time.Second)))
		t.EtaAt = &eta
	}
	if u.State != "" {
		t.State = u.State
	}
	if t.State.IsTerminal() {
		ended := now
		t.EndedAt, t.EtaAt = &ended, nil
	}
	t.Stale = false
	t.UpdatedAt = now
	t.Revision++
	return t
}

func isRunning(t AgentTask) bool { return t.State == TaskRunning }
func isAuto(t AgentTask) bool    { return t.Source == SourceAuto }
func isEndedOK(t AgentTask) bool {
	return t.State == TaskSucceeded || t.State == TaskCancelled
}

// taskClass is a bound class (see MaxTasksPerClass). Agent and auto tasks
// are counted apart, so neither source can crowd out the other.
type taskClass int

const (
	classAgentRunning taskClass = iota
	classAgentFailed
	classAgentEndedOK
	classAutoRunning
	classAutoEnded
)

func classOf(t AgentTask) taskClass {
	switch {
	case isAuto(t) && isRunning(t):
		return classAutoRunning
	case isAuto(t):
		return classAutoEnded
	case isRunning(t):
		return classAgentRunning
	case t.State == TaskFailed:
		return classAgentFailed
	default:
		return classAgentEndedOK
	}
}

func (s *ProgressStore) countLocked(agent string, class taskClass) int {
	n := 0
	for _, e := range s.agents[agent] {
		if classOf(e.task) == class {
			n++
		}
	}
	return n
}

// enforceBoundLocked drops the oldest ended tasks of keep's class until the
// class is within MaxTasksPerClass. Running tasks are capped at admission.
func (s *ProgressStore) enforceBoundLocked(agent string, keep *taskEntry) {
	if isRunning(keep.task) {
		return
	}
	class := classOf(keep.task)
	for s.countLocked(agent, class) > MaxTasksPerClass {
		var oldest *taskEntry
		for _, e := range s.agents[agent] {
			if e == keep || classOf(e.task) != class {
				continue
			}
			if oldest == nil || e.task.EndedAt.Before(*oldest.task.EndedAt) {
				oldest = e
			}
		}
		s.removeLocked(oldest)
	}
}

func (s *ProgressStore) entriesLocked(agent string) []*taskEntry {
	out := make([]*taskEntry, 0, len(s.agents[agent]))
	for _, e := range s.agents[agent] {
		out = append(out, e)
	}
	return out
}

func (s *ProgressStore) tasksLocked(agent string) []AgentTask {
	entries := s.entriesLocked(agent)
	if len(entries) == 0 {
		return nil
	}
	slices.SortFunc(entries, func(a, b *taskEntry) int {
		if c := a.task.StartedAt.Compare(b.task.StartedAt); c != 0 {
			return c
		}
		return compareStrings(a.task.ID, b.task.ID)
	})
	out := make([]AgentTask, len(entries))
	for i, e := range entries {
		out[i] = e.task.clone()
	}
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// armLocked (re)arms e's lifecycle timer for its current state: a running
// task goes stale StaleAfter after its last update, a succeeded or
// cancelled one is removed ExpireAfter after it ended, a failed one has no
// timer.
func (s *ProgressStore) armLocked(e *taskEntry) {
	s.disarmLocked(e)
	var deadline time.Time
	var fire func(*taskEntry)
	switch {
	case isAuto(e.task) && isRunning(e.task):
		return // auto tasks are never stale; their owner ends them
	case isAuto(e.task):
		deadline, fire = e.task.EndedAt.Add(AutoExpireAfter), s.removeLocked
	case e.task.State == TaskRunning:
		deadline, fire = e.task.UpdatedAt.Add(StaleAfter), s.markStaleLocked
	case isEndedOK(e.task):
		deadline, fire = e.task.EndedAt.Add(ExpireAfter), s.removeLocked
	default:
		return
	}
	gen := e.gen
	e.stopTimer = s.clock.AfterFunc(max(0, deadline.Sub(s.clock.Now())), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.gen == gen {
			fire(e)
		}
	})
}

// disarmLocked cancels e's timer and invalidates one already firing.
func (s *ProgressStore) disarmLocked(e *taskEntry) {
	e.gen++
	if e.stopTimer != nil {
		e.stopTimer()
		e.stopTimer = nil
	}
}

func (s *ProgressStore) markStaleLocked(e *taskEntry) {
	e.task.Stale = true
	e.task.Revision++
	e.stopTimer = nil
	s.publishLocked(e)
}

// removeLocked deletes e and publishes its removal.
func (s *ProgressStore) removeLocked(e *taskEntry) {
	s.disarmLocked(e)
	s.cancelPendingLocked(e)
	delete(s.agents[e.agent], e.task.ID)
	if len(s.agents[e.agent]) == 0 {
		delete(s.agents, e.agent)
	}
	s.emitLocked(e.agent, e.task.ID, nil)
}

// publishLocked announces e's current state, coalesced: the first write
// after a quiet TaskPublishInterval goes out at once (leading edge); writes
// inside the window collapse into one trailing publish of the latest state.
func (s *ProgressStore) publishLocked(e *taskEntry) {
	if e.pending {
		return
	}
	wait := TaskPublishInterval - s.clock.Now().Sub(e.lastPublish)
	if wait <= 0 {
		s.flushLocked(e)
		return
	}
	e.pending = true
	e.flushGen++
	gen := e.flushGen
	e.stopFlush = s.clock.AfterFunc(wait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.pending && e.flushGen == gen {
			s.flushLocked(e)
		}
	})
}

// flushLocked publishes e's current state now and cancels any held-back
// trailing publish it supersedes.
func (s *ProgressStore) flushLocked(e *taskEntry) {
	s.cancelPendingLocked(e)
	e.lastPublish = s.clock.Now()
	t := e.task.clone()
	s.emitLocked(e.agent, e.task.ID, &t)
}

func (s *ProgressStore) cancelPendingLocked(e *taskEntry) {
	e.flushGen++
	e.pending = false
	if e.stopFlush != nil {
		e.stopFlush()
		e.stopFlush = nil
	}
}

func (s *ProgressStore) emitLocked(agent, id string, task *AgentTask) {
	if s.publisher == nil {
		return
	}
	s.publisher.Publish(Event{
		Type:    EventAgentTaskChanged,
		Payload: &AgentTaskChangedPayload{Agent: agent, ID: id, Task: task},
	})
}

// clone copies t including its optional-value pointers.
func (t AgentTask) clone() AgentTask {
	if t.Percent != nil {
		p := *t.Percent
		t.Percent = &p
	}
	if t.EtaAt != nil {
		e := *t.EtaAt
		t.EtaAt = &e
	}
	if t.EndedAt != nil {
		e := *t.EndedAt
		t.EndedAt = &e
	}
	return t
}
