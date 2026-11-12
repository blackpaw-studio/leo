package observe

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	// DefaultCodexPollInterval is how often the poller reads every running
	// codex agent's rollout.
	DefaultCodexPollInterval = 10 * time.Second
	// codexRewalkInterval bounds how often a rollout that cannot be found is
	// searched for again.
	codexRewalkInterval = time.Minute
	// codexReadsPerTick bounds the capped reads one agent gets per tick, so
	// catching up on a large rollout cannot hold the tick.
	codexReadsPerTick = 4
)

// CodexAgent is one running codex agent as the poller needs it.
type CodexAgent struct {
	Name string
	// Home is the agent's $CODEX_HOME.
	Home string
	// SessionID is the codex session id leo stored for the agent; empty until
	// leo has delivered its first message.
	SessionID string
	Workspace string
	// StartedAt is when the agent's current process started.
	StartedAt time.Time
}

// CodexAgentLister lists the codex agents worth polling: running ones only.
type CodexAgentLister interface {
	CodexAgents() []CodexAgent
}

// HarnessUsageSink receives each poll's complete result, by agent name.
type HarnessUsageSink interface {
	SetHarnessUsage(usage map[string]AgentUsage)
}

// CodexLocators finds rollout files; the daemon wires them to the codex
// harness, which owns the file layout.
type CodexLocators struct {
	// ByID is the rollout of a session id under home, "" when absent.
	ByID func(home, id string) string
	// Latest is the newest rollout for workspace modified at/after since,
	// with how many qualified.
	Latest func(home, workspace string, since time.Time) (path, id string, matches int)
}

// CodexPollerOptions tunes a CodexPoller; zero values take the defaults.
type CodexPollerOptions struct {
	Interval time.Duration
	// Now replaces the wall clock (tests).
	Now func() time.Time
	// NewTicker replaces the wall-clock ticker (tests).
	NewTicker func(d time.Duration) (<-chan time.Time, func())
}

// CodexPoller reads each running codex agent's rollout on an interval,
// folds it into usage (tokens, context, model, effort, rate limits) and
// hands the lot to a HarnessUsageSink. A tick that finds the previous one
// still running is skipped. It never persists anything it discovers.
type CodexPoller struct {
	lister CodexAgentLister
	loc    CodexLocators
	sink   HarnessUsageSink
	opts   CodexPollerOptions

	polling atomic.Bool
	// states is per agent name; only the poll holding the polling flag
	// touches it.
	states map[string]*codexAgentState
}

// codexAgentState is what the poller keeps for one agent between ticks.
type codexAgentState struct {
	startedAt time.Time
	home      string

	// path is the rollout in use; sid its session id; idKey the stored id it
	// was found for ("" when found by workspace).
	path, sid, idKey string
	// walkKey and lastWalk throttle searching for a rollout that is not there.
	walkKey  string
	lastWalk time.Time

	tail *rolloutTail
	fold *codexRolloutFold
	// last is the previous reading, kept across a read that failed.
	last    AgentUsage
	hasLast bool
}

// NewCodexPoller builds a poller over lister, locating rollouts through loc
// and publishing to sink.
func NewCodexPoller(lister CodexAgentLister, loc CodexLocators, sink HarnessUsageSink, opts CodexPollerOptions) *CodexPoller {
	if opts.Interval <= 0 {
		opts.Interval = DefaultCodexPollInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewTicker == nil {
		opts.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &CodexPoller{lister: lister, loc: loc, sink: sink, opts: opts, states: map[string]*codexAgentState{}}
}

// Run polls on every tick until ctx ends.
func (p *CodexPoller) Run(ctx context.Context) {
	ticks, stop := p.opts.NewTicker(p.opts.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			p.Tick(ctx)
		}
	}
}

// Tick polls once, returning false without publishing when the previous poll
// is still in flight or ctx ended.
func (p *CodexPoller) Tick(ctx context.Context) bool {
	if !p.polling.CompareAndSwap(false, true) {
		return false
	}
	defer p.polling.Store(false)

	now := p.opts.Now()
	out := map[string]AgentUsage{}
	live := map[string]*codexAgentState{}
	for _, a := range p.lister.CodexAgents() {
		if ctx.Err() != nil {
			return false
		}
		st := p.stateFor(a)
		live[a.Name] = st
		if u, ok := p.poll(st, a, now); ok {
			out[a.Name] = u
		}
	}
	if ctx.Err() != nil {
		return false
	}
	p.states = live
	p.sink.SetHarnessUsage(out)
	return true
}

// stateFor is a's state, fresh when it is new or its process restarted (the
// incarnation is counted from the rollout again; the account's rate limits
// carry over).
func (p *CodexPoller) stateFor(a CodexAgent) *codexAgentState {
	prev := p.states[a.Name]
	if prev != nil && prev.startedAt.Equal(a.StartedAt) && prev.home == a.Home {
		return prev
	}
	st := &codexAgentState{startedAt: a.StartedAt, home: a.Home, tail: &rolloutTail{}, fold: newCodexRolloutFold(a.StartedAt)}
	if prev != nil {
		st.fold.rateLimits = prev.fold.rateLimits
	}
	return st
}

// poll reads a's rollout and returns its usage; ok=false omits the agent
// (no rollout, or nothing in it to report yet).
func (p *CodexPoller) poll(st *codexAgentState, a CodexAgent, now time.Time) (AgentUsage, bool) {
	path, ok := p.locate(st, a, now)
	if !ok {
		return AgentUsage{}, false
	}
	if path != st.tail.path {
		st.tail = &rolloutTail{path: path}
		st.fold.Reset()
	}
	for range codexReadsPerTick {
		res := st.tail.Read(maxRolloutRead)
		if !res.OK {
			// Transient or gone: keep the last reading, and look the rollout
			// up again next tick.
			st.path = ""
			return st.last, st.hasLast
		}
		if res.Reset {
			st.fold.Reset()
		}
		for _, line := range res.Lines {
			st.fold.AddLine(line)
		}
		if !res.More {
			break
		}
	}
	u := st.fold.Usage(st.sid)
	if u.Model == "" && u.Context == nil && len(u.RateLimits) == 0 && u.Session.Tokens == 0 {
		return AgentUsage{}, false
	}
	st.last, st.hasLast = u, true
	return u, true
}

// locate finds the rollout to read: the stored session id's, else (for an
// agent leo has not stored an id for) the single rollout for its workspace
// since it started. A found path is kept until reading it fails.
func (p *CodexPoller) locate(st *codexAgentState, a CodexAgent, now time.Time) (string, bool) {
	if st.path != "" && st.idKey == a.SessionID {
		return st.path, true
	}
	if a.SessionID == "" && st.path != "" {
		return st.path, true
	}
	key := "id:" + a.SessionID
	if a.SessionID == "" {
		key = "ws:" + a.Workspace
	}
	if st.walkKey == key && now.Sub(st.lastWalk) < codexRewalkInterval {
		return "", false
	}
	st.walkKey, st.lastWalk = key, now
	if a.SessionID != "" {
		path := p.loc.ByID(a.Home, a.SessionID)
		if path == "" {
			return "", false
		}
		st.path, st.sid, st.idKey = path, a.SessionID, a.SessionID
		return path, true
	}
	path, id, matches := p.loc.Latest(a.Home, a.Workspace, a.StartedAt)
	if matches != 1 || path == "" {
		return "", false
	}
	st.path, st.sid, st.idKey = path, id, ""
	return path, true
}
