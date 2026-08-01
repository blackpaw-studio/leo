package observe

import (
	"context"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// defaultSweepInterval and defaultIdleThreshold are the tracker's out-of-the-
// box cadence; both are overridable via TrackerOption for tests and tuning.
const (
	defaultSweepInterval = 2 * time.Second
	defaultIdleThreshold = 15 * time.Second
)

// listSessionActivityFn is the seam tests replace for tmux.ListSessionActivity.
var listSessionActivityFn = tmux.ListSessionActivity

// TrackerOption configures a Tracker at construction time.
type TrackerOption func(*Tracker)

// WithSweepInterval overrides the default 2s sweep cadence.
func WithSweepInterval(d time.Duration) TrackerOption {
	return func(t *Tracker) { t.interval = d }
}

// WithIdleThreshold overrides the default 15s idle threshold.
func WithIdleThreshold(d time.Duration) TrackerOption {
	return func(t *Tracker) { t.idleThreshold = d }
}

// WithClock overrides time.Now for deterministic tests.
func WithClock(now func() time.Time) TrackerOption {
	return func(t *Tracker) { t.now = now }
}

// Tracker sweeps tmux session activity for every supervised agent and
// derives each one's live Activity, satisfying observe.ActivityProvider.
type Tracker struct {
	mu         sync.RWMutex
	activities map[string]AgentActivity

	tmuxPath      string
	sessionNames  func() map[string]string
	publisher     Publisher
	interval      time.Duration
	idleThreshold time.Duration
	now           func() time.Time
}

// NewTracker creates a Tracker. sessionNames returns the current agent-name
// to tmux-session-name mapping the tracker should sweep; publisher may be nil
// (safe no-op). Call Start in a goroutine to begin sweeping.
func NewTracker(tmuxPath string, sessionNames func() map[string]string, publisher Publisher, opts ...TrackerOption) *Tracker {
	t := &Tracker{
		activities:    make(map[string]AgentActivity),
		tmuxPath:      tmuxPath,
		sessionNames:  sessionNames,
		publisher:     publisher,
		interval:      defaultSweepInterval,
		idleThreshold: defaultIdleThreshold,
		now:           time.Now,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Start runs the sweep loop until ctx is done. Callers run it in a goroutine.
func (t *Tracker) Start(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.sweep(ctx)
		}
	}
}

// Activities implements observe.ActivityProvider, returning a defensive copy.
func (t *Tracker) Activities() map[string]AgentActivity {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]AgentActivity, len(t.activities))
	for k, v := range t.activities {
		if v.CurrentAction != nil {
			cp := *v.CurrentAction
			v.CurrentAction = &cp
		}
		out[k] = v
	}
	return out
}

// sweep runs one pass: one tmux.ListSessionActivity call covers the whole
// fleet, then a capture-pane is issued only for agents that just started
// working, so idle agents cost nothing.
func (t *Tracker) sweep(ctx context.Context) {
	sessions := t.sessionNames()
	tmuxActivity, err := listSessionActivityFn(ctx, t.tmuxPath)
	if err != nil {
		return
	}
	now := t.now()

	t.mu.RLock()
	prevSnapshot := t.activities
	t.mu.RUnlock()

	type sampleTarget struct{ agent, session string }
	next := make(map[string]AgentActivity, len(sessions))
	var toSample []sampleTarget

	for agentName, sessionName := range sessions {
		prev, hadPrev := prevSnapshot[agentName]
		sess, hasSess := tmuxActivity[sessionName]
		if !hasSess {
			next[agentName] = AgentActivity{Activity: ActivityUnknown}
			continue
		}
		result, shouldSample := t.classify(now, hadPrev, prev, sess)
		if shouldSample {
			toSample = append(toSample, sampleTarget{agentName, sessionName})
		}
		next[agentName] = result
	}

	for _, target := range toSample {
		v := next[target.agent]
		v.CurrentAction = capturePaneAction(ctx, t.tmuxPath, target.session)
		next[target.agent] = v
	}

	t.mu.Lock()
	var changed []changedActivity
	for agentName, v := range next {
		prev, hadPrev := t.activities[agentName]
		if !hadPrev || prev.Activity != v.Activity || !sameAction(prev.CurrentAction, v.CurrentAction) {
			changed = append(changed, changedActivity{agentName, v})
		}
	}
	t.activities = next
	t.mu.Unlock()

	for _, c := range changed {
		t.publish(c.agent, c.reading)
	}
}

// classify derives one agent's next AgentActivity reading from its previous
// reading and the tmux session's raw activity, plus whether a pane sample
// should be taken this sweep (only agents that just started or resumed
// working).
func (t *Tracker) classify(now time.Time, hadPrev bool, prev AgentActivity, sess tmux.SessionActivity) (AgentActivity, bool) {
	// A prior Unknown reading carries no meaningful LastActivityAt to compare
	// against, so treat it the same as never having seen this agent before.
	fresh := !hadPrev || prev.Activity == ActivityUnknown
	if fresh {
		if now.Sub(sess.LastActivity) >= t.idleThreshold {
			return AgentActivity{Activity: ActivityIdle, LastActivityAt: sess.LastActivity}, false
		}
		return AgentActivity{Activity: ActivityWorking, LastActivityAt: sess.LastActivity}, true
	}

	if sess.LastActivity.After(prev.LastActivityAt) {
		return AgentActivity{Activity: ActivityWorking, LastActivityAt: sess.LastActivity}, true
	}
	if now.Sub(prev.LastActivityAt) >= t.idleThreshold {
		// The action is only ever sampled while working (see sweep's
		// toSample pass), so it must not survive the transition to idle —
		// otherwise an agent that finished working long ago would report a
		// stale "currently doing" indefinitely.
		return AgentActivity{Activity: ActivityIdle, LastActivityAt: prev.LastActivityAt}, false
	}
	return AgentActivity{Activity: ActivityWorking, LastActivityAt: prev.LastActivityAt, CurrentAction: prev.CurrentAction}, false
}

// changedActivity pairs an agent name with its newly settled reading, for the
// publish pass run after the state lock is released.
type changedActivity struct {
	agent   string
	reading AgentActivity
}

// publish is a nil-safe no-op when the tracker has no publisher. The
// CurrentAction handed to the publisher is a copy, not the *Action stored in
// t.activities — a subscriber must never be able to mutate the tracker's own
// state through the pointer on a delivered event.
func (t *Tracker) publish(agentName string, a AgentActivity) {
	if t.publisher == nil {
		return
	}
	var action *Action
	if a.CurrentAction != nil {
		cp := *a.CurrentAction
		action = &cp
	}
	t.publisher.Publish(Event{
		Type: EventAgentActivity,
		Payload: &AgentActivityPayload{
			Agent:         agentName,
			Activity:      a.Activity,
			CurrentAction: action,
		},
	})
}

// sameAction reports whether two Action pointers carry equal content,
// treating two nils as equal.
func sameAction(a, b *Action) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
