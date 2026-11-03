package consult

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// BackgroundTask is a background Bash task a run started and has not seen
// end. The tracker is the second witness of background work: under a bridge
// whose mod never sees Claude's Stop hook (every session with its own
// CLAUDE_CONFIG_DIR) the turn.complete carries no pending work, and the
// PostToolUse that started the task is the only record of it.
type BackgroundTask struct {
	ID        string    `json:"id"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	Command   string    `json:"command,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

const (
	// maxTrackedTasks bounds the tracker, which grows from model-driven tool
	// calls.
	maxTrackedTasks = 64
	// maxTrackedCommandRunes bounds the command kept for display.
	maxTrackedCommandRunes = 200
)

// taskStopTools are the tools that end a background task by id.
var taskStopTools = []string{"TaskStop", "KillShell", "KillBash"}

// taskStopIDFields name the id a task-stopping tool call carries.
var taskStopIDFields = []string{"task_id", "shell_id"}

var notificationTaskID = regexp.MustCompile(`<task-id>\s*([^<\s]+)\s*</task-id>`)

// taskGoneErrors are the substrings of the errors Claude Code's TaskStop
// refuses with when the task it names is no longer running (its
// StopTaskError table): such a refusal says the task is over.
var taskGoneErrors = []string{"no task found with id", "task not found", " is not running (status:"}

// taskGoneError reports whether a task-stopping call's error says its task
// is already gone, rather than that the stop was refused for a live task.
func taskGoneError(msg string) bool {
	lower := strings.ToLower(msg)
	return slices.ContainsFunc(taskGoneErrors, func(s string) bool { return strings.Contains(lower, s) })
}

// taskStopID is the task id a task-stopping tool call names.
func taskStopID(p map[string]any) string {
	input, _ := p["tool_input"].(map[string]any)
	for _, field := range taskStopIDFields {
		if id := str(input, field); id != "" {
			return id
		}
	}
	return ""
}

// trackDecision is one keep-or-prune call the tracker made on a tracked
// task, for the daemon log. It carries ids and a fixed reason only: never a
// command, prompt, or error text.
type trackDecision struct {
	TaskID   string
	Decision string // decisionKept or decisionPruned
	Reason   string // one of the reason* constants
}

const (
	decisionKept   = "kept"
	decisionPruned = "pruned"

	reasonStarted          = "started"
	reasonStopOK           = "stop-ok"
	reasonStopFailedGone   = "stop-failed-gone"
	reasonStopFailedOther  = "stop-failed-other"
	reasonTaskNotification = "task-notification"
	reasonStopOmitted      = "stop-omitted"
	reasonStopListed       = "stop-listed"
	reasonAdopted          = "adopted"
)

func pruned(id, reason string) trackDecision {
	return trackDecision{TaskID: id, Decision: decisionPruned, Reason: reason}
}

func kept(id, reason string) trackDecision {
	return trackDecision{TaskID: id, Decision: decisionKept, Reason: reason}
}

// prunedCount is how many tracked tasks the decisions pruned.
func prunedCount(decisions []trackDecision) int {
	n := 0
	for _, d := range decisions {
		if d.Decision == decisionPruned {
			n++
		}
	}
	return n
}

// trackToolEvent returns tasks updated for one PostToolUse or
// PostToolUseFailure payload: a Bash call that started background work adds
// its task, a task-stopping call removes it, and so does one refused because
// its task is already gone. tasks is never mutated.
func trackToolEvent(tasks []BackgroundTask, event string, p map[string]any, now time.Time) ([]BackgroundTask, []trackDecision) {
	tool := str(p, "tool_name")
	if event == "posttoolusefailure" {
		id := taskStopID(p)
		if !slices.Contains(taskStopTools, tool) || !isTracked(tasks, id) {
			return tasks, nil
		}
		if taskGoneError(str(p, "error")) {
			return withoutTask(tasks, id), []trackDecision{pruned(id, reasonStopFailedGone)}
		}
		return tasks, []trackDecision{kept(id, reasonStopFailedOther)}
	}
	if event != "posttooluse" {
		return tasks, nil
	}
	switch {
	case tool == "Bash":
		resp, _ := p["tool_response"].(map[string]any)
		id := str(resp, "backgroundTaskId")
		if id == "" || isTracked(tasks, id) || len(tasks) >= maxTrackedTasks {
			return tasks, nil
		}
		input, _ := p["tool_input"].(map[string]any)
		return append(slices.Clone(tasks), BackgroundTask{
			ID:        id,
			ToolUseID: str(p, "tool_use_id"),
			Command:   truncateRunes(str(input, "command"), maxTrackedCommandRunes),
			StartedAt: now,
		}), []trackDecision{kept(id, reasonStarted)}
	case slices.Contains(taskStopTools, tool):
		if id := taskStopID(p); isTracked(tasks, id) {
			return withoutTask(tasks, id), []trackDecision{pruned(id, reasonStopOK)}
		}
	}
	return tasks, nil
}

// trackPrompt returns tasks without the task a harness injection announces
// as finished. A prompt a person typed is never a task ending, whatever it
// quotes.
func trackPrompt(tasks []BackgroundTask, prompt string) ([]BackgroundTask, []trackDecision) {
	if len(tasks) == 0 || !isHarnessInjection(prompt) {
		return tasks, nil
	}
	m := notificationTaskID.FindStringSubmatch(prompt)
	if m == nil || !isTracked(tasks, m[1]) {
		return tasks, nil
	}
	return withoutTask(tasks, m[1]), []trackDecision{pruned(m[1], reasonTaskNotification)}
}

// trackStop returns tasks pruned to the ones a Stop payload lists. A payload
// without a task list, or one with an entry that names no id (the bridge's
// counts-only rendition), says nothing about which tasks ended.
func trackStop(tasks []BackgroundTask, p map[string]any) ([]BackgroundTask, []trackDecision) {
	return reconcileStop(tasks, p, time.Time{}, false)
}

// reconcileStop is trackStop that can also adopt: with adopt set, a listed
// running shell the tracker missed (its PostToolUse was lost) is added at now.
// Only the superseded shell Stop adopts: a Stop the dispatcher applies whole
// already carries its own pending work, and adopting from it would keep a
// task alive that only a notification clears.
func reconcileStop(tasks []BackgroundTask, p map[string]any, now time.Time, adopt bool) ([]BackgroundTask, []trackDecision) {
	listed, ok := p["background_tasks"].([]any)
	if !ok || (len(tasks) == 0 && !adopt) {
		return tasks, nil
	}
	live := map[string]bool{}
	var missing []BackgroundTask
	for _, raw := range listed {
		entry, _ := raw.(map[string]any)
		id := str(entry, "id")
		if id == "" {
			return tasks, nil
		}
		live[id] = true
		if adopt && isRunningShell(entry) && !isTracked(tasks, id) && !isTracked(missing, id) {
			missing = append(missing, BackgroundTask{ID: id, Command: truncateRunes(firstNonEmpty(str(entry, "command"), str(entry, "description")), maxTrackedCommandRunes), StartedAt: now})
		}
	}
	var decisions []trackDecision
	for _, t := range tasks {
		if live[t.ID] {
			decisions = append(decisions, kept(t.ID, reasonStopListed))
		} else {
			decisions = append(decisions, pruned(t.ID, reasonStopOmitted))
		}
	}
	keptTasks := slices.DeleteFunc(slices.Clone(tasks), func(t BackgroundTask) bool { return !live[t.ID] })
	adopted := missing[:min(len(missing), max(0, maxTrackedTasks-len(keptTasks)))]
	for _, t := range adopted {
		decisions = append(decisions, kept(t.ID, reasonAdopted))
	}
	keptTasks = append(keptTasks, adopted...)
	if len(keptTasks) == 0 {
		return nil, decisions
	}
	return keptTasks, decisions
}

func isTracked(tasks []BackgroundTask, id string) bool {
	return id != "" && slices.ContainsFunc(tasks, func(t BackgroundTask) bool { return t.ID == id })
}

// isRunningShell reports whether a Stop's task entry is in-flight shell work,
// the only kind the tracker holds (other kinds ride the Stop's own counts).
func isRunningShell(entry map[string]any) bool {
	return sanitizeTaskType(str(entry, "type")) == "shell" && !settledTaskStatuses[strings.ToLower(str(entry, "status"))]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func withoutTask(tasks []BackgroundTask, id string) []BackgroundTask {
	if !slices.ContainsFunc(tasks, func(t BackgroundTask) bool { return t.ID == id }) {
		return tasks
	}
	kept := slices.DeleteFunc(slices.Clone(tasks), func(t BackgroundTask) bool { return t.ID == id })
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// withTrackedTasks is the pending work a Stop leaves: what the Stop listed
// together with the tracked tasks. A shell both name counts once, so the
// shell count is the larger of the two.
func withTrackedTasks(w *PendingWork, tasks []BackgroundTask) *PendingWork {
	if len(tasks) == 0 {
		return w
	}
	merged := clonePendingWork(w)
	if merged == nil {
		merged = &PendingWork{}
	}
	if merged.Tasks == nil {
		merged.Tasks = map[string]int{}
	}
	merged.Tasks["shell"] = max(merged.Tasks["shell"], len(tasks))
	return merged
}

func truncateRunes(s string, limit int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return strings.TrimSpace(s)
}

func cloneBackgroundTasks(tasks []BackgroundTask) []BackgroundTask { return slices.Clone(tasks) }

// ReportSupersededStop applies what a shell Stop hook says about background
// tasks when a bridge owns the run's turn state and so drops that Stop's
// turn meaning: its task list reconciles the tracker, adding a shell whose
// PostToolUse was lost and pruning ended ones. Only the tracker changes: a
// turn the bridge's turn.complete already closed stays closed, since
// reopening it would collide with its completion notification and any
// release_on_finish it triggered. Any superseded hook also notes where the
// session's transcript is, which only hooks say (see
// noteLiveTranscriptLocked).
func (d *Dispatcher) ReportSupersededStop(id string, r HookReport) error {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Mode != ModeInteractive || s.record.Status.Terminal() || s.releasing {
		return nil
	}
	var p map[string]any
	if json.Unmarshal(r.Payload, &p) != nil {
		return nil
	}
	if noteLiveTranscriptLocked(s, p) {
		d.persistLocked(s, "")
	}
	if normalizeHookEvent(str(p, "hook_event_name")) != "stop" {
		return nil
	}
	before := pendingSnapshot(s)
	tasks, decisions := reconcileStop(s.record.BackgroundTasks, p, d.now(), true)
	d.prunePendingLocked(s, before, d.trackLocked(s, "stop", tasks, decisions))
	s.hookStop = stashHookStop(pendingWorkFromStop(p), d.now())
	return nil
}

// hookStopWork is the pending work a superseded hook Stop reported, kept for
// the bridge turn.complete that ends the same turn. The mod cannot report it
// itself: claude's classic.Stop hook is one cc-plugin-sec-default passes over.
type hookStopWork struct {
	work *PendingWork
	at   time.Time
}

// stashHookStop is the stash a Stop leaves: its pending work, or nothing when
// it left none (which also drops an older Stop's).
func stashHookStop(work *PendingWork, now time.Time) *hookStopWork {
	if work == nil {
		return nil
	}
	return &hookStopWork{work: work, at: now}
}

// takeHookStopWorkLocked hands a bridge turn.complete the pending work the
// hook Stop before it left, if that is recent enough to be the same turn's.
func (d *Dispatcher) takeHookStopWorkLocked(s *runState) *PendingWork {
	stash := s.hookStop
	s.hookStop = nil
	if stash == nil || d.now().Sub(stash.at) >= hookStopFreshness {
		return nil
	}
	return stash.work
}

// hookStopFreshness is how long a superseded hook Stop's pending work waits
// for the bridge turn.complete it belongs to (see ReportSupersededStop).
const hookStopFreshness = 10 * time.Second

// trackLocked applies one tracker update for event and returns how many
// tracked tasks it pruned, for prunePendingLocked.
func (d *Dispatcher) trackLocked(s *runState, event string, tasks []BackgroundTask, decisions []trackDecision) int {
	d.setTrackedLocked(s, tasks, decisions, event)
	return prunedCount(decisions)
}

// setTrackedLocked replaces the run's tracked tasks, persisting a change,
// and logs each decision the tracker made on the way for event.
func (d *Dispatcher) setTrackedLocked(s *runState, tasks []BackgroundTask, decisions []trackDecision, event string) {
	d.logTrackDecisionsLocked(s.record.ID, event, decisions)
	if slices.EqualFunc(s.record.BackgroundTasks, tasks, func(a, b BackgroundTask) bool { return a == b }) {
		return
	}
	s.record.BackgroundTasks = tasks
	d.persistLocked(s, "")
}

// logTrackDecisionsLocked writes one daemon-log line per tracker decision.
// Every field is an id, a hook event name, or a fixed word: nothing the model
// or a person wrote reaches the log.
func (d *Dispatcher) logTrackDecisionsLocked(id, event string, decisions []trackDecision) {
	w := d.trackLog
	if w == nil {
		w = os.Stderr
	}
	for _, dec := range decisions {
		fmt.Fprintf(w, "dispatch %s: tracker %s task=%s %s reason=%s\n", id, event, sanitizeTaskID(dec.TaskID), dec.Decision, dec.Reason)
	}
}

// maxLoggedTaskIDRunes bounds a task id in the daemon log.
const maxLoggedTaskIDRunes = 64

// sanitizeTaskID is a task id safe for one daemon-log line: ids come from
// hook payloads, so anything outside an id's alphabet becomes '?' and the
// length is bounded.
func sanitizeTaskID(id string) string {
	r := []rune(id)
	if len(r) > maxLoggedTaskIDRunes {
		r = r[:maxLoggedTaskIDRunes]
	}
	return strings.Map(func(c rune) rune {
		if c == '-' || c == '_' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return c
		}
		return '?'
	}, string(r))
}

// pendingSnapshot is each waiting turn's pending work before an event's
// handler runs, so a prune afterwards lowers only the waits the event left
// alone: one the event itself set (a Stop pausing a turn) already reflects
// the pruned tracker.
func pendingSnapshot(s *runState) map[string]*PendingWork {
	var before map[string]*PendingWork
	for _, t := range s.record.Turns {
		if t.Outcome == "" && t.Pending != nil {
			if before == nil {
				before = map[string]*PendingWork{}
			}
			before[t.TurnID] = t.Pending
		}
	}
	return before
}

// prunePendingLocked carries a tracker prune into the run's turns: each wait
// the event left untouched loses the pruned shells, and a turn left with
// nothing pending ends as its background work finishing would have ended
// it, keeping what its Stops said. The run's status is re-derived.
func (d *Dispatcher) prunePendingLocked(s *runState, before map[string]*PendingWork, pruned int) {
	if pruned == 0 || len(before) == 0 || s.releasing || s.record.Status.Terminal() || s.record.Status == StatusSettling {
		return
	}
	old := s.record.Status
	var emptied []string
	changed := false
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome != "" || t.Pending == nil || t.Pending != before[t.TurnID] {
			continue
		}
		w := withoutShells(t.Pending, pruned)
		if w == t.Pending {
			continue
		}
		changed = true
		if w == nil {
			emptied = append(emptied, t.TurnID)
			continue
		}
		t.Pending = w
	}
	if !changed {
		return
	}
	for _, id := range emptied {
		d.closeTurnLocked(s, id, TurnFinished, "")
	}
	d.recomputeStatusLocked(s, d.now())
	if s.record.Status != old {
		d.persistLocked(s, "status")
	} else {
		d.persistLocked(s, "")
	}
}

// withoutShells returns w with n fewer shells (never below zero), nil when
// nothing is left pending, or w itself when it counts no shells. w is never
// mutated: turns may share it.
func withoutShells(w *PendingWork, n int) *PendingWork {
	if w == nil || w.Tasks["shell"] == 0 {
		return w
	}
	lowered := clonePendingWork(w)
	if left := w.Tasks["shell"] - n; left > 0 {
		lowered.Tasks["shell"] = left
	} else {
		delete(lowered.Tasks, "shell")
	}
	if len(lowered.Tasks) == 0 && lowered.Wakeups == 0 {
		return nil
	}
	return lowered
}
