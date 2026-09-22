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
}

// NewAttentionStore creates an empty store. publisher may be nil.
func NewAttentionStore(publisher Publisher) *AttentionStore {
	return &AttentionStore{
		states:    make(map[string]AttentionState),
		revisions: make(map[string]uint64),
		tokens:    make(map[string]string),
		publisher: publisher,
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

func (s *AttentionStore) setLocked(agent string, state AttentionState) AgentAttention {
	s.revisions[agent]++
	s.states[agent] = state
	att := AgentAttention{State: state, Revision: s.revisions[agent]}
	s.publishLocked(agent, att)
	return att
}

// Remove drops agent's attention (the field becomes absent) and its tokens
// while keeping its revision counter, so a later Set under the same name
// still moves forward.
func (s *AttentionStore) Remove(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.states, agent)
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
	state, ok := s.states[oldName]
	if !ok {
		return
	}
	rev := max(s.revisions[oldName], s.revisions[newName]) + 1
	delete(s.states, oldName)
	s.states[newName] = state
	s.revisions[newName] = rev
	s.publishLocked(newName, AgentAttention{State: state, Revision: rev})
}

// Get returns agent's current attention, if it has an attention source.
func (s *AttentionStore) Get(agent string) (AgentAttention, bool) {
	if s == nil {
		return AgentAttention{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[agent]
	if !ok {
		return AgentAttention{}, false
	}
	return AgentAttention{State: state, Revision: s.revisions[agent]}, true
}

// All returns a copy of every agent's current attention.
func (s *AttentionStore) All() map[string]AgentAttention {
	if s == nil {
		return map[string]AgentAttention{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AgentAttention, len(s.states))
	for agent, state := range s.states {
		out[agent] = AgentAttention{State: state, Revision: s.revisions[agent]}
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
