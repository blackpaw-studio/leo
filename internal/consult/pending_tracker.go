package consult

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/bgshell"
)

// BackgroundTask is a background Bash task a run started and has not seen
// end (see bgshell.Task).
type BackgroundTask = bgshell.Task

// trackDecision is one keep-or-prune call the tracker made on a tracked task.
type trackDecision = bgshell.Decision

// The tracker's rules live in bgshell, shared with the auto progress tasks.
func trackToolEvent(tasks []BackgroundTask, event string, p map[string]any, now time.Time) ([]BackgroundTask, []trackDecision) {
	return bgshell.TrackToolEvent(tasks, event, p, now)
}

func trackPrompt(tasks []BackgroundTask, prompt string) ([]BackgroundTask, []trackDecision) {
	return bgshell.TrackPrompt(tasks, prompt)
}

func trackStop(tasks []BackgroundTask, p map[string]any) ([]BackgroundTask, []trackDecision) {
	return bgshell.TrackStop(tasks, p)
}

func reconcileStop(tasks []BackgroundTask, p map[string]any, now time.Time, adopt bool) ([]BackgroundTask, []trackDecision) {
	return bgshell.ReconcileStop(tasks, p, now, adopt)
}

func prunedIDs(decisions []trackDecision) []string { return bgshell.PrunedIDs(decisions) }

func trackedTaskIDs(tasks []BackgroundTask) []string { return bgshell.TaskIDs(tasks) }

func isHarnessInjection(prompt string) bool { return bgshell.IsHarnessInjection(prompt) }

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

// trackLocked applies one tracker update for event and returns the tracked
// tasks it pruned, for prunePendingLocked.
func (d *Dispatcher) trackLocked(s *runState, event string, tasks []BackgroundTask, decisions []trackDecision) []string {
	d.setTrackedLocked(s, tasks, decisions, event)
	return prunedIDs(decisions)
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
// the event left untouched loses the shells it counted among the pruned
// tasks, and a turn left with nothing pending ends as its background work
// finishing would have ended it, keeping what its Stops said. The run's
// status is re-derived.
func (d *Dispatcher) prunePendingLocked(s *runState, before map[string]*PendingWork, pruned []string) {
	if len(pruned) == 0 || len(before) == 0 || s.releasing || s.record.Status.Terminal() || s.record.Status == StatusSettling {
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
		owned := slices.DeleteFunc(slices.Clone(t.PendingTaskIDs), func(id string) bool { return !slices.Contains(pruned, id) })
		if len(owned) == 0 {
			continue
		}
		t.PendingTaskIDs = slices.DeleteFunc(slices.Clone(t.PendingTaskIDs), func(id string) bool { return slices.Contains(owned, id) })
		changed = true
		w := withoutShells(t.Pending, len(owned))
		if w == t.Pending {
			continue
		}
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
