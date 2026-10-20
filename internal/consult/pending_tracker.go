package consult

import (
	"encoding/json"
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

// trackToolEvent returns tasks updated for one PostToolUse payload: a Bash
// call that started background work adds its task, a task-stopping call
// removes it. tasks is never mutated.
func trackToolEvent(tasks []BackgroundTask, event string, p map[string]any, now time.Time) []BackgroundTask {
	if event != "posttooluse" {
		return tasks
	}
	switch tool := str(p, "tool_name"); {
	case tool == "Bash":
		resp, _ := p["tool_response"].(map[string]any)
		id := str(resp, "backgroundTaskId")
		if id == "" || slices.ContainsFunc(tasks, func(t BackgroundTask) bool { return t.ID == id }) || len(tasks) >= maxTrackedTasks {
			return tasks
		}
		input, _ := p["tool_input"].(map[string]any)
		return append(slices.Clone(tasks), BackgroundTask{
			ID:        id,
			ToolUseID: str(p, "tool_use_id"),
			Command:   truncateRunes(str(input, "command"), maxTrackedCommandRunes),
			StartedAt: now,
		})
	case slices.Contains(taskStopTools, tool):
		input, _ := p["tool_input"].(map[string]any)
		for _, field := range taskStopIDFields {
			if id := str(input, field); id != "" {
				return withoutTask(tasks, id)
			}
		}
	}
	return tasks
}

// trackPrompt returns tasks without the task a harness injection announces
// as finished. A prompt a person typed is never a task ending, whatever it
// quotes.
func trackPrompt(tasks []BackgroundTask, prompt string) []BackgroundTask {
	if len(tasks) == 0 || !isHarnessInjection(prompt) {
		return tasks
	}
	m := notificationTaskID.FindStringSubmatch(prompt)
	if m == nil {
		return tasks
	}
	return withoutTask(tasks, m[1])
}

// trackStop returns tasks pruned to the ones a Stop payload lists. A payload
// without a task list, or one with an entry that names no id (the bridge's
// counts-only rendition), says nothing about which tasks ended.
func trackStop(tasks []BackgroundTask, p map[string]any) []BackgroundTask {
	return reconcileStop(tasks, p, time.Time{}, false)
}

// reconcileStop is trackStop that can also adopt: with adopt set, a listed
// running shell the tracker missed (its PostToolUse was lost) is added at now.
// Only the superseded shell Stop adopts: a Stop the dispatcher applies whole
// already carries its own pending work, and adopting from it would keep a
// task alive that only a notification clears.
func reconcileStop(tasks []BackgroundTask, p map[string]any, now time.Time, adopt bool) []BackgroundTask {
	listed, ok := p["background_tasks"].([]any)
	if !ok || (len(tasks) == 0 && !adopt) {
		return tasks
	}
	live := map[string]bool{}
	var missing []BackgroundTask
	for _, raw := range listed {
		entry, _ := raw.(map[string]any)
		id := str(entry, "id")
		if id == "" {
			return tasks
		}
		live[id] = true
		if adopt && isRunningShell(entry) && !slices.ContainsFunc(tasks, func(t BackgroundTask) bool { return t.ID == id }) {
			missing = append(missing, BackgroundTask{ID: id, Command: truncateRunes(firstNonEmpty(str(entry, "command"), str(entry, "description")), maxTrackedCommandRunes), StartedAt: now})
		}
	}
	kept := slices.DeleteFunc(slices.Clone(tasks), func(t BackgroundTask) bool { return !live[t.ID] })
	kept = append(kept, missing[:min(len(missing), max(0, maxTrackedTasks-len(kept)))]...)
	if len(kept) == 0 {
		return nil
	}
	return kept
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
	d.setTrackedLocked(s, reconcileStop(s.record.BackgroundTasks, p, d.now(), true))
	return nil
}

// setTrackedLocked replaces the run's tracked tasks, persisting a change.
func (d *Dispatcher) setTrackedLocked(s *runState, tasks []BackgroundTask) {
	if slices.EqualFunc(s.record.BackgroundTasks, tasks, func(a, b BackgroundTask) bool { return a == b }) {
		return
	}
	s.record.BackgroundTasks = tasks
	d.persistLocked(s, "")
}
