package observe

import "sync"

// RunLog is a bounded, newest-first, in-memory record of task runs. It
// implements Publisher itself and wraps another Publisher (typically the
// event bus), recording every TaskRunPayload as it passes through and
// forwarding every event — run-related or not — to the wrapped Publisher
// unchanged.
//
// RunLog is deliberately not a bus subscriber: the bus drops slow
// subscribers rather than blocking a publisher, and a run log that could
// silently lose events would make the snapshot's recent_runs untrustworthy.
// Wiring it in front of the bus (SetPublisher(runLog), runLog wraps the bus)
// makes recording synchronous with publish, so it can never miss an event.
type RunLog struct {
	mu       sync.Mutex
	next     Publisher
	capacity int
	// runs is newest-first. A running run already present (matched by ID) is
	// updated in place rather than appended again once its finish event
	// arrives, so one firing never occupies two slots.
	runs []TaskRun
}

// NewRunLog creates a RunLog wrapping next (nil is a valid "record only, no
// forwarding" configuration), bounded to capacity entries. capacity <= 0
// uses MaxRecentRuns.
func NewRunLog(next Publisher, capacity int) *RunLog {
	if capacity <= 0 {
		capacity = MaxRecentRuns
	}
	return &RunLog{next: next, capacity: capacity}
}

// Publish records ev if it carries a TaskRunPayload, then forwards ev
// unchanged to the wrapped Publisher, if any.
func (l *RunLog) Publish(ev Event) {
	if p, ok := ev.Payload.(*TaskRunPayload); ok {
		l.record(p.Run)
	}
	if l.next != nil {
		l.next.Publish(ev)
	}
}

// record inserts or updates run by ID, keeping runs newest-first and
// trimming to capacity.
func (l *RunLog) record(run TaskRun) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i, existing := range l.runs {
		if existing.ID == run.ID {
			l.runs[i] = run
			return
		}
	}

	l.runs = append([]TaskRun{run}, l.runs...)
	if len(l.runs) > l.capacity {
		l.runs = l.runs[:l.capacity]
	}
}

// Recent returns up to n runs, newest first, as defensive copies safe for
// the caller to mutate or retain. n <= 0 returns every run currently held.
func (l *RunLog) Recent(n int) []TaskRun {
	l.mu.Lock()
	defer l.mu.Unlock()

	if n <= 0 || n > len(l.runs) {
		n = len(l.runs)
	}
	out := make([]TaskRun, n)
	copy(out, l.runs[:n])
	return out
}
