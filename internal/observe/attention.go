package observe

import (
	"slices"
	"sync"
)

// AttentionState is an agent's semantic turn state, fed by harness hooks and
// supervisor lifecycle transitions — never by pane sampling.
type AttentionState string

const (
	AttentionWorking    AttentionState = "working"
	AttentionNeedsInput AttentionState = "needs_input"
	AttentionFinished   AttentionState = "finished"
	AttentionErrored    AttentionState = "errored"
	AttentionUnknown    AttentionState = "unknown"
)

// AgentAttention is the wire shape of an agent's attention. Revision is
// strictly increasing per agent within one daemon lifetime; consumers ignore
// a revision they have already seen or passed.
type AgentAttention struct {
	State    AttentionState `json:"state"`
	Revision uint64         `json:"revision"`
	// Reason says what a needs_input agent is waiting on; absent otherwise
	// or when the source cannot tell.
	Reason *AttentionReason `json:"reason,omitempty"`
	// Outstanding explains a working state held after the turn completed
	// (B-051); absent when nothing is outstanding.
	Outstanding *Outstanding `json:"outstanding,omitempty"`
}

// AttentionReasonKind is what a needs_input agent is waiting on.
type AttentionReasonKind string

const (
	AttentionReasonPermission  AttentionReasonKind = "permission"
	AttentionReasonQuestion    AttentionReasonKind = "question"
	AttentionReasonElicitation AttentionReasonKind = "elicitation"
)

// AttentionReason details a needs_input state. Tool and Detail are
// mod-supplied display text, clamped by ClampAttentionReason.
type AttentionReason struct {
	Kind   AttentionReasonKind `json:"kind"`
	Tool   string              `json:"tool,omitempty"`
	Detail string              `json:"detail,omitempty"`
}

// AttentionStore holds every agent's attention state. Each Set is a semantic
// transition: it bumps that agent's revision (even for a repeated state) and
// publishes an agent_activity event carrying the agent's latest activity
// reading. Revisions survive Remove so a re-created agent never replays an
// old revision. All methods are nil-safe no-ops, so an unwired store is
// indistinguishable from "no attention source".
type AttentionStore struct {
	mu        sync.Mutex
	states    map[string]AttentionState
	revisions map[string]uint64
	// tokens maps each live launch's attention token to its agent's current
	// name. Hooks address an agent only through its token, so a rename
	// follows the agent and a stopped launch's late hooks go nowhere.
	tokens    map[string]string
	publisher Publisher
	activity  ActivityProvider

	// outstanding counts each agent's running children, kept whether or
	// not the agent is tracked yet. held marks a finished deferred by
	// them (B-051): the agent shows working until the count reaches 0.
	outstanding map[string]Outstanding
	held        map[string]bool
	// reasons holds what a needs_input agent is waiting on, when known.
	reasons map[string]AttentionReason
}

// NewAttentionStore creates an empty store. publisher may be nil.
func NewAttentionStore(publisher Publisher) *AttentionStore {
	return &AttentionStore{
		states:    make(map[string]AttentionState),
		revisions: make(map[string]uint64),
		tokens:    make(map[string]string),
		publisher: publisher,

		outstanding: make(map[string]Outstanding),
		held:        make(map[string]bool),
		reasons:     make(map[string]AttentionReason),
	}
}

// SetActivityProvider wires the tracker whose reading rides along on every
// published event. A setter rather than a constructor argument because the
// tracker itself is built with this store (WithAttention).
func (s *AttentionStore) SetActivityProvider(p ActivityProvider) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.activity = p
	s.mu.Unlock()
}

// Set records a transition for agent and returns the new attention.
func (s *AttentionStore) Set(agent string, state AttentionState) AgentAttention {
	if s == nil {
		return AgentAttention{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(agent, state)
}

// SetIfTracked is Set, but only for an agent that already has attention —
// lifecycle transitions (errored, stopped) must not invent a source for an
// agent whose harness reports none.
func (s *AttentionStore) SetIfTracked(agent string, state AttentionState) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[agent]; !ok {
		return AgentAttention{}, false
	}
	return s.setLocked(agent, state), true
}

// SetIfUntracked is Set, but only for an agent with no attention yet — a
// launch's initial state must not clobber a preset or a hook that already
// landed. ok=false means the agent was tracked and nothing changed.
func (s *AttentionStore) SetIfUntracked(agent string, state AttentionState) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[agent]; ok {
		return AgentAttention{}, false
	}
	return s.setLocked(agent, state), true
}

// setLocked records one transition. A finished while children are
// outstanding is held as working (B-051) until the last one ends; a new
// turn or a lifecycle transition drops the hold, but answering a prompt
// raised during it (needs_input → working) keeps it.
func (s *AttentionStore) setLocked(agent string, state AttentionState) AgentAttention {
	prev := s.states[agent]
	switch {
	case state == AttentionFinished && s.outstanding[agent].total() > 0:
		s.held[agent] = true
		state = AttentionWorking
	case state == AttentionWorking && prev == AttentionNeedsInput:
	case state != AttentionNeedsInput:
		delete(s.held, agent)
	}
	if state != AttentionNeedsInput {
		delete(s.reasons, agent)
	}
	s.revisions[agent]++
	s.states[agent] = state
	att := s.attentionLocked(agent)
	s.publishLocked(agent, att)
	return att
}

// attentionLocked is agent's current wire attention; agent must be tracked.
func (s *AttentionStore) attentionLocked(agent string) AgentAttention {
	att := AgentAttention{State: s.states[agent], Revision: s.revisions[agent]}
	if r, ok := s.reasons[agent]; ok {
		att.Reason = &r
	}
	if o := s.outstanding[agent]; o.total() > 0 {
		att.Outstanding = &o
	}
	return att
}

// Advance is SetIfTracked for a source that may repeat what another already
// reported (the claude mod bridge alongside the harness hooks): a
// transition to the current state is skipped, as is a finished already
// held, and a working that repeats a held one only drops the hold (a new
// turn began). With from, it applies only from one of those states.
// ok=false means nothing was published.
func (s *AttentionStore) Advance(agent string, state AttentionState, from ...AttentionState) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, tracked := s.states[agent]
	if !tracked || len(from) > 0 && !slices.Contains(from, cur) {
		return AgentAttention{}, false
	}
	switch {
	case state == AttentionWorking && cur == AttentionWorking:
		delete(s.held, agent)
		return AgentAttention{}, false
	case state == AttentionFinished && cur == AttentionWorking && s.outstanding[agent].total() > 0:
		s.held[agent] = true
		return AgentAttention{}, false
	case state == cur:
		return AgentAttention{}, false
	}
	return s.setLocked(agent, state), true
}

// AdvanceNeedsInput moves a tracked agent to needs_input with reason
// (clamped), unless it is already there for the same reason.
func (s *AttentionStore) AdvanceNeedsInput(agent string, reason AttentionReason) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	reason = ClampAttentionReason(reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, tracked := s.states[agent]
	if !tracked {
		return AgentAttention{}, false
	}
	if prev, ok := s.reasons[agent]; cur == AttentionNeedsInput && ok && prev == reason {
		return AgentAttention{}, false
	}
	s.reasons[agent] = reason
	return s.setLocked(agent, AttentionNeedsInput), true
}

// SetOutstandingDispatches records how many leo dispatches agent has
// running. See setOutstanding.
func (s *AttentionStore) SetOutstandingDispatches(agent string, n int) {
	s.setOutstanding(agent, func(o *Outstanding) { o.Dispatches = max(n, 0) })
}

// SetOutstandingSubagents records how many native background subagents
// agent has running. See setOutstanding.
func (s *AttentionStore) SetOutstandingSubagents(agent string, n int) {
	s.setOutstanding(agent, func(o *Outstanding) { o.Subagents = max(n, 0) })
}

// setOutstanding applies one source's count. A change for a tracked agent
// publishes it under a new revision; the last child ending releases a held
// finished. Counts for an untracked agent are kept for when it is.
func (s *AttentionStore) setOutstanding(agent string, apply func(*Outstanding)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.outstanding[agent]
	next := prev
	apply(&next)
	if next == prev {
		return
	}
	if next.total() == 0 {
		delete(s.outstanding, agent)
	} else {
		s.outstanding[agent] = next
	}
	if _, tracked := s.states[agent]; !tracked {
		return
	}
	if next.total() == 0 && s.held[agent] {
		s.setLocked(agent, AttentionFinished)
		return
	}
	s.revisions[agent]++
	s.publishLocked(agent, s.attentionLocked(agent))
}

func (o Outstanding) total() int { return o.Dispatches + o.Subagents }

// Remove drops agent's attention (the field becomes absent) and its tokens
// while keeping its revision counter, so a later Set under the same name
// still moves forward.
func (s *AttentionStore) Remove(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.states, agent)
	delete(s.held, agent)
	delete(s.reasons, agent)
	s.unregisterAgentLocked(agent)
	s.mu.Unlock()
}

// RegisterToken routes hooks carrying token to agent. An empty token is
// ignored.
func (s *AttentionStore) RegisterToken(token, agent string) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	s.tokens[token] = agent
	s.mu.Unlock()
}

// UnregisterToken stops routing token, leaving the agent's other tokens.
func (s *AttentionStore) UnregisterToken(token string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// UnregisterAgent stops routing every token of agent.
func (s *AttentionStore) UnregisterAgent(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.unregisterAgentLocked(agent)
	s.mu.Unlock()
}

func (s *AttentionStore) unregisterAgentLocked(agent string) {
	for token, name := range s.tokens {
		if name == agent {
			delete(s.tokens, token)
		}
	}
}

// AgentForToken returns the agent token currently routes to.
func (s *AttentionStore) AgentForToken(token string) (string, bool) {
	if s == nil || token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.tokens[token]
	return name, ok
}

// SetByToken is Set for the agent token routes to, resolved and applied
// atomically so an unregister (stop, exit, delete) always wins over a late
// hook. When from is non-empty the transition applies only while the
// agent's current state is one of from; a skipped transition changes
// nothing. ok=false means no transition was recorded.
func (s *AttentionStore) SetByToken(token string, state AttentionState, from ...AttentionState) (AgentAttention, bool) {
	if s == nil || token == "" {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.tokens[token]
	if !ok {
		return AgentAttention{}, false
	}
	if len(from) > 0 && !slices.Contains(from, s.states[agent]) {
		return AgentAttention{}, false
	}
	return s.setLocked(agent, state), true
}

// Move re-keys an agent's attention and tokens after a rename and announces
// the attention under the new name so a stream consumer learns the carried
// state. The revision bumps past both names' counters: a consumer may
// already have seen newName at its own (possibly higher) revision. Tokens
// move even when oldName has no attention yet.
func (s *AttentionStore) Move(oldName, newName string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, name := range s.tokens {
		if name == oldName {
			s.tokens[token] = newName
		}
	}
	moveKey(s.outstanding, oldName, newName)
	state, ok := s.states[oldName]
	if !ok {
		return
	}
	moveKey(s.held, oldName, newName)
	moveKey(s.reasons, oldName, newName)
	rev := max(s.revisions[oldName], s.revisions[newName]) + 1
	delete(s.states, oldName)
	s.states[newName] = state
	s.revisions[newName] = rev
	s.publishLocked(newName, s.attentionLocked(newName))
}

// Get returns agent's current attention, if it has an attention source.
func (s *AttentionStore) Get(agent string) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[agent]; !ok {
		return AgentAttention{}, false
	}
	return s.attentionLocked(agent), true
}

// All returns a copy of every agent's current attention.
func (s *AttentionStore) All() map[string]AgentAttention {
	if s == nil {
		return map[string]AgentAttention{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AgentAttention, len(s.states))
	for agent := range s.states {
		out[agent] = s.attentionLocked(agent)
	}
	return out
}

// publishLocked runs under s.mu so events leave in revision order. Safe:
// publishers never block (the bus drops slow subscribers) and the tracker
// never takes s.mu while holding its own lock.
func (s *AttentionStore) publishLocked(agent string, att AgentAttention) {
	if s.publisher == nil {
		return
	}
	reading := AgentActivity{Activity: ActivityUnknown}
	if s.activity != nil {
		if a, ok := s.activity.Activities()[agent]; ok {
			reading = a
		}
	}
	s.publisher.Publish(Event{
		Type: EventAgentActivity,
		Payload: &AgentActivityPayload{
			Agent:         agent,
			Activity:      reading.Activity,
			CurrentAction: reading.CurrentAction,
			Attention:     &att,
		},
	})
}

// moveKey re-keys m[from] to to, dropping any value to already had.
func moveKey[V any](m map[string]V, from, to string) {
	v, ok := m[from]
	delete(m, to)
	if ok {
		delete(m, from)
		m[to] = v
	}
}
