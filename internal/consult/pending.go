package consult

import (
	"fmt"
	"sort"
	"strings"
)

// StatusWaiting marks an interactive run whose harness ended its turn while
// background work it started (a shell, a monitor, a scheduled wakeup) is
// still pending: the turn stays open, since that work will wake the session
// and carry the turn on.
const StatusWaiting Status = "waiting"

// PendingWork summarizes the background work a waiting run is paused on, as
// its harness reported it when the turn stopped.
type PendingWork struct {
	// Tasks counts in-flight background tasks by type (shell, monitor,
	// subagent, workflow, …).
	Tasks map[string]int `json:"tasks,omitempty"`
	// Wakeups counts session crons (ScheduleWakeup, CronCreate, /loop) that
	// will wake the session later.
	Wakeups int `json:"wakeups,omitempty"`
}

// pendingTaskOrder is the order Summary lists the common task types in;
// any other type follows, alphabetically.
var pendingTaskOrder = []string{"shell", "monitor", "subagent", "workflow"}

// settledTaskStatuses are background-task statuses that no longer wake the
// session. Claude lists only in-flight work, so these are defensive.
var settledTaskStatuses = map[string]bool{"completed": true, "failed": true, "killed": true, "stopped": true, "canceled": true, "cancelled": true}

// maxPendingTypeLen bounds a task type taken from a hook payload.
const maxPendingTypeLen = 32

// pendingWorkFromStop reads a Stop payload's background_tasks and
// session_crons: nil when neither lists anything pending (absent, as older
// claude versions send, or malformed).
func pendingWorkFromStop(p map[string]any) *PendingWork {
	var w PendingWork
	tasks, _ := p["background_tasks"].([]any)
	for _, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok || settledTaskStatuses[strings.ToLower(str(task, "status"))] {
			continue
		}
		kind := sanitizeTaskType(str(task, "type"))
		if w.Tasks == nil {
			w.Tasks = map[string]int{}
		}
		w.Tasks[kind]++
	}
	crons, _ := p["session_crons"].([]any)
	for _, raw := range crons {
		if _, ok := raw.(map[string]any); ok {
			w.Wakeups++
		}
	}
	if len(w.Tasks) == 0 && w.Wakeups == 0 {
		return nil
	}
	return &w
}

func sanitizeTaskType(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	var b strings.Builder
	for _, r := range kind {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' {
			b.WriteByte('_')
		}
		if b.Len() >= maxPendingTypeLen {
			break
		}
	}
	if b.Len() == 0 {
		return "other"
	}
	return b.String()
}

// Summary is the compact form the roster and lists show, e.g.
// "1 shell · 1 monitor · 2 wakeups".
func (w *PendingWork) Summary() string {
	if w == nil {
		return ""
	}
	var parts []string
	seen := map[string]bool{}
	for _, kind := range pendingTaskOrder {
		if n := w.Tasks[kind]; n > 0 {
			parts = append(parts, countNoun(n, kind))
		}
		seen[kind] = true
	}
	rest := make([]string, 0, len(w.Tasks))
	for kind, n := range w.Tasks {
		if !seen[kind] && n > 0 {
			rest = append(rest, kind)
		}
	}
	sort.Strings(rest)
	for _, kind := range rest {
		parts = append(parts, countNoun(w.Tasks[kind], kind))
	}
	if w.Wakeups > 0 {
		parts = append(parts, countNoun(w.Wakeups, "wakeup"))
	}
	return strings.Join(parts, " · ")
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	if strings.HasSuffix(noun, "ch") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func clonePendingWork(w *PendingWork) *PendingWork {
	if w == nil {
		return nil
	}
	c := PendingWork{Wakeups: w.Wakeups}
	if w.Tasks != nil {
		c.Tasks = make(map[string]int, len(w.Tasks))
		for k, v := range w.Tasks {
			c.Tasks[k] = v
		}
	}
	return &c
}
