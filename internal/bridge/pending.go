package bridge

import (
	"bytes"
	"encoding/json"
)

// PendingWork is the background work a main-loop turn left in flight when
// it ended, as the mod read it off Claude's Stop hook: in-flight background
// tasks counted by type, and session crons that will wake the session.
type PendingWork struct {
	Tasks   map[string]int
	Wakeups int
}

// Bounds on a mod-supplied pending report: the dispatcher expands it into
// one hook entry per count.
const (
	maxPendingTypes    = 16
	maxPendingTypeLen  = 32
	maxPendingPerCount = 1000
)

type wirePending struct {
	Tasks   map[string]int `json:"tasks"`
	Wakeups int            `json:"wakeups"`
}

// parsePending strictly decodes a turn.complete's pending: an object with
// optional tasks (type → count) and wakeups, nothing else. Absent is nil;
// null or anything malformed is refused.
func parsePending(fields map[string]json.RawMessage) (*PendingWork, error) {
	raw, present := fields["pending"]
	if !present {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wirePending
	if isNull(raw) || dec.Decode(&w) != nil {
		return nil, invalidReport("pending must be an object of tasks (type → count) and wakeups")
	}
	if len(w.Tasks) > maxPendingTypes {
		return nil, invalidReport("pending lists more than %d task types", maxPendingTypes)
	}
	for kind, n := range w.Tasks {
		if !validPendingType(kind) || n < 0 || n > maxPendingPerCount {
			return nil, invalidReport("pending tasks must map a type of [a-z0-9_-] to a count of 0..%d", maxPendingPerCount)
		}
	}
	if w.Wakeups < 0 || w.Wakeups > maxPendingPerCount {
		return nil, invalidReport("pending wakeups must be a count of 0..%d", maxPendingPerCount)
	}
	return &PendingWork{Tasks: w.Tasks, Wakeups: w.Wakeups}, nil
}

func validPendingType(kind string) bool {
	if kind == "" || len(kind) > maxPendingTypeLen {
		return false
	}
	for _, c := range []byte(kind) {
		switch {
		case 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// stopHookFields is p in the shape Claude's Stop hook posts it: one
// background_tasks entry per in-flight task and one session_crons entry
// per wakeup.
func (p *PendingWork) stopHookFields() (tasks []map[string]string, crons []map[string]string) {
	if p == nil {
		return nil, nil
	}
	tasks = []map[string]string{}
	for kind, n := range p.Tasks {
		for range n {
			tasks = append(tasks, map[string]string{"type": kind, "status": "running"})
		}
	}
	crons = make([]map[string]string, p.Wakeups)
	for i := range crons {
		crons[i] = map[string]string{}
	}
	return tasks, crons
}

func clonePending(p *PendingWork) *PendingWork {
	if p == nil {
		return nil
	}
	c := PendingWork{Wakeups: p.Wakeups}
	if p.Tasks != nil {
		c.Tasks = make(map[string]int, len(p.Tasks))
		for k, v := range p.Tasks {
			c.Tasks[k] = v
		}
	}
	return &c
}
