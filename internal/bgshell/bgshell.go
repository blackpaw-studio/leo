// Package bgshell tracks the background Bash shells an agent's Claude Code
// session starts: which are running and when each ends, read from the hook and
// prompt payloads Claude sends. It is pure (no clock, no I/O) so the dispatch
// pending-tracker and the auto progress tasks share one set of rules.
package bgshell

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// Task is a background Bash task a run started and has not seen
// end. The tracker is the second witness of background work: under a bridge
// whose mod never sees Claude's Stop hook (every session with its own
// CLAUDE_CONFIG_DIR) the turn.complete carries no pending work, and the
// PostToolUse that started the task is the only record of it.
type Task struct {
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
// refuses with when the task it names is no longer running: "No task found
// with ID: <id>[. Did you mean: …]" and "Task <id> is not running (status:
// <s>)" (2.1.317). Claude checks both in validateInput too, and a refusal
// there fires no hook at all; only one raised from the tool call itself (the
// task ended in between) reaches leo, as PostToolUseFailure's error field.
var taskGoneErrors = []string{"no task found with id", " is not running (status:"}

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

// Decision is one keep-or-prune call the tracker made on a tracked
// task, for the daemon log. It carries ids and a fixed reason only: never a
// command, prompt, or error text.
type Decision struct {
	TaskID   string
	Decision string // DecisionKept or DecisionPruned
	Reason   string // one of the reason* constants
}

const (
	DecisionKept   = "kept"
	DecisionPruned = "pruned"

	reasonStarted          = "started"
	reasonStopOK           = "stop-ok"
	reasonStopFailedGone   = "stop-failed-gone"
	reasonStopFailedOther  = "stop-failed-other"
	reasonTaskNotification = "task-notification"
	reasonStopOmitted      = "stop-omitted"
	reasonStopListed       = "stop-listed"
	reasonAdopted          = "adopted"
)

func pruned(id, reason string) Decision {
	return Decision{TaskID: id, Decision: DecisionPruned, Reason: reason}
}

func kept(id, reason string) Decision {
	return Decision{TaskID: id, Decision: DecisionKept, Reason: reason}
}

// PrunedIDs are the tracked tasks the decisions pruned.
func PrunedIDs(decisions []Decision) []string {
	var ids []string
	for _, d := range decisions {
		if d.Decision == DecisionPruned {
			ids = append(ids, d.TaskID)
		}
	}
	return ids
}

// TaskIDs are the ids of tasks, nil when there are none.
func TaskIDs(tasks []Task) []string {
	var ids []string
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	return ids
}

// TrackToolEvent returns tasks updated for one PostToolUse or
// PostToolUseFailure payload: a Bash call that started background work adds
// its task, a task-stopping call removes it, and so does one refused because
// its task is already gone. tasks is never mutated.
func TrackToolEvent(tasks []Task, event string, p map[string]any, now time.Time) ([]Task, []Decision) {
	tool := str(p, "tool_name")
	if event == "posttoolusefailure" {
		id := taskStopID(p)
		if !slices.Contains(taskStopTools, tool) || !IsTracked(tasks, id) {
			return tasks, nil
		}
		if taskGoneError(str(p, "error")) {
			return WithoutTask(tasks, id), []Decision{pruned(id, reasonStopFailedGone)}
		}
		return tasks, []Decision{kept(id, reasonStopFailedOther)}
	}
	if event != "posttooluse" {
		return tasks, nil
	}
	switch {
	case tool == "Bash":
		resp, _ := p["tool_response"].(map[string]any)
		id := str(resp, "backgroundTaskId")
		if id == "" || IsTracked(tasks, id) || len(tasks) >= maxTrackedTasks {
			return tasks, nil
		}
		input, _ := p["tool_input"].(map[string]any)
		return append(slices.Clone(tasks), Task{
			ID:        id,
			ToolUseID: str(p, "tool_use_id"),
			Command:   TruncateRunes(str(input, "command"), maxTrackedCommandRunes),
			StartedAt: now,
		}), []Decision{kept(id, reasonStarted)}
	case slices.Contains(taskStopTools, tool):
		if id := taskStopID(p); IsTracked(tasks, id) {
			return WithoutTask(tasks, id), []Decision{pruned(id, reasonStopOK)}
		}
	}
	return tasks, nil
}

// TrackPrompt returns tasks without the task a harness injection announces
// as finished. A prompt a person typed is never a task ending, whatever it
// quotes.
func TrackPrompt(tasks []Task, prompt string) ([]Task, []Decision) {
	if len(tasks) == 0 || !IsHarnessInjection(prompt) {
		return tasks, nil
	}
	m := notificationTaskID.FindStringSubmatch(prompt)
	if len(m) < 2 || !IsTracked(tasks, m[1]) {
		return tasks, nil
	}
	return WithoutTask(tasks, m[1]), []Decision{pruned(m[1], reasonTaskNotification)}
}

// TrackStop returns tasks pruned to the ones a Stop payload lists. A payload
// without a task list, or one with an entry that names no id (the bridge's
// counts-only rendition), says nothing about which tasks ended.
func TrackStop(tasks []Task, p map[string]any) ([]Task, []Decision) {
	return ReconcileStop(tasks, p, time.Time{}, false)
}

// ReconcileStop is TrackStop that can also adopt: with adopt set, a listed
// running shell the tracker missed (its PostToolUse was lost) is added at now.
// Only the superseded shell Stop adopts: a Stop the dispatcher applies whole
// already carries its own pending work, and adopting from it would keep a
// task alive that only a notification clears.
func ReconcileStop(tasks []Task, p map[string]any, now time.Time, adopt bool) ([]Task, []Decision) {
	listed, ok := p["background_tasks"].([]any)
	if !ok || (len(tasks) == 0 && !adopt) {
		return tasks, nil
	}
	live := map[string]bool{}
	var missing []Task
	for _, raw := range listed {
		entry, _ := raw.(map[string]any)
		id := str(entry, "id")
		if id == "" {
			return tasks, nil
		}
		live[id] = true
		if adopt && isRunningShell(entry) && !IsTracked(tasks, id) && !IsTracked(missing, id) {
			missing = append(missing, Task{ID: id, Command: TruncateRunes(firstNonEmpty(str(entry, "command"), str(entry, "description")), maxTrackedCommandRunes), StartedAt: now})
		}
	}
	var decisions []Decision
	for _, t := range tasks {
		if live[t.ID] {
			decisions = append(decisions, kept(t.ID, reasonStopListed))
		} else {
			decisions = append(decisions, pruned(t.ID, reasonStopOmitted))
		}
	}
	keptTasks := slices.DeleteFunc(slices.Clone(tasks), func(t Task) bool { return !live[t.ID] })
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

func IsTracked(tasks []Task, id string) bool {
	return id != "" && slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id })
}

// isRunningShell reports whether a Stop's task entry is in-flight shell work,
// the only kind the tracker holds (other kinds ride the Stop's own counts).
func isRunningShell(entry map[string]any) bool {
	return SanitizeTaskType(str(entry, "type")) == "shell" && !Settled(str(entry, "status"))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func WithoutTask(tasks []Task, id string) []Task {
	if !slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id }) {
		return tasks
	}
	kept := slices.DeleteFunc(slices.Clone(tasks), func(t Task) bool { return t.ID == id })
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// TruncateRunes trims s and cuts it to limit runes, marking a cut with an ellipsis.
func TruncateRunes(s string, limit int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return strings.TrimSpace(s)
}

// ReportSupersededStop applies what a shell Stop hook says about background
// tasks when a bridge owns the run's turn state and so drops that Stop's
func str(p map[string]any, k string) string { v, _ := p[k].(string); return v }

// Str is the string value of key k in a hook payload, or "".
func Str(p map[string]any, k string) string { return str(p, k) }

// settledStatuses are background-task statuses that no longer wake the
// session. Claude lists only in-flight work, so these are defensive.
var settledStatuses = map[string]bool{"completed": true, "failed": true, "killed": true, "stopped": true, "canceled": true, "cancelled": true}

// Settled reports whether a Stop task entry's status says it has ended.
func Settled(status string) bool { return settledStatuses[strings.ToLower(status)] }

// maxTypeLen bounds a task type taken from a hook payload.
const maxTypeLen = 32

// SanitizeTaskType is a task type reduced to [a-z0-9_-], "other" when nothing
// is left.
func SanitizeTaskType(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	var b strings.Builder
	for _, r := range kind {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' {
			b.WriteByte('_')
		}
		if b.Len() >= maxTypeLen {
			break
		}
	}
	if b.Len() == 0 {
		return "other"
	}
	return b.String()
}

// harnessInjectionMarkers are the envelope tags of prompts the harness
// submits on its own, which fire UserPromptSubmit like a typed prompt.
var harnessInjectionMarkers = []string{
	"task-notification", // Claude Code: a background task finished
}

// IsHarnessInjection reports whether the whole prompt is one injection
// envelope, so a human quoting a tag still counts as typing.
func IsHarnessInjection(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	for _, m := range harnessInjectionMarkers {
		if strings.HasPrefix(prompt, "<"+m+">") && strings.HasSuffix(prompt, "</"+m+">") {
			return true
		}
	}
	return false
}
