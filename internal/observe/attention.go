package observe

import "sync"

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
	publisher Publisher
	activity  ActivityProvider
}

// NewAttentionStore creates an empty store. publisher may be nil.
func NewAttentionStore(publisher Publisher) *AttentionStore {
	return &AttentionStore{
		states:    make(map[string]AttentionState),
		revisions: make(map[string]uint64),
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
	s.revisions[agent]++
	s.states[agent] = state
	att := AgentAttention{State: state, Revision: s.revisions[agent]}
	s.publishLocked(agent, att)
	return att
}

// Remove drops agent's attention (the field becomes absent) while keeping its
// revision counter, so a later Set under the same name still moves forward.
func (s *AttentionStore) Remove(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.states, agent)
	s.mu.Unlock()
}

// Move re-keys an agent's attention after a rename without bumping it, and
// announces it under the new name so a stream consumer learns the carried
// state. A no-op when oldName has no attention.
func (s *AttentionStore) Move(oldName, newName string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[oldName]
	if !ok {
		return
	}
	rev := max(s.revisions[oldName], s.revisions[newName])
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
