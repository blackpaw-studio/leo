package brief

import "sync"

// NudgeEveryTurns is how many completed turns pass between reminders to keep
// the brief current.
const NudgeEveryTurns = 10

// Tracker counts each agent's completed turns since its brief was last
// written or last nudged about, in memory: a daemon restart resets it, so a
// nudge may come late, never twice.
type Tracker struct {
	mu    sync.Mutex
	turns map[string]int
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{turns: map[string]int{}} }

// TurnCompleted records one finished turn of agent.
func (t *Tracker) TurnCompleted(agent string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns[agent]++
}

// Wrote restarts agent's count: its brief is current.
func (t *Tracker) Wrote(agent string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.turns, agent)
}

// Nudge reports whether agent is due a reminder, and if so restarts its
// count so the next is NudgeEveryTurns turns away.
func (t *Tracker) Nudge(agent string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turns[agent] < NudgeEveryTurns {
		return false
	}
	delete(t.turns, agent)
	return true
}

// Forget drops agent's count, for a deleted agent.
func (t *Tracker) Forget(agent string) { t.Wrote(agent) }
