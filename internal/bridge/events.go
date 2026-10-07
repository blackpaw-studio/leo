package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Event is a hello, turn/session, or observe event as seen by a Subscriber. Name is
// ReportHello or one of the Event* names. SessionID and ClaudeVersion come
// from the agent's latest hello (the event's own, for a hello).
type Event struct {
	Agent string
	// Gen is the generation (see Target) whose launch reported it.
	Gen           uint64
	Name          string
	SessionID     string
	ClaudeVersion string
	Usage         json.RawMessage
	Reason        string
	Prompt        string      // turn.start
	Message       string      // turn.complete
	EventID       string      // stable across retries of one event; may be empty
	Tokens        *TurnTokens // turn.complete: the turn's own token counts
	// Observe event payloads (see Report): exactly the one matching Name is
	// set, except that a hello may carry Subagents (the mod's running
	// count). The observability projection reads these; nothing else does.
	Activity  *ActivityReport
	Attention *AttentionReport
	Subagents *SubagentsReport
	Compact   *CompactReport
	At        time.Time
}

// Subscriber is told about every hello and turn/session event: the seam
// that feeds dispatch turn state (see HookPayload).
type Subscriber interface {
	OnBridgeEvent(Event)
}

// SubscriberFunc adapts a function to Subscriber.
type SubscriberFunc func(Event)

// OnBridgeEvent calls f.
func (f SubscriberFunc) OnBridgeEvent(ev Event) { f(ev) }

// State is a point-in-time copy of one agent's bridge state.
type State struct {
	Agent string
	// Gen is the key's current generation (see Target); 0 while the key is
	// forgotten.
	Gen         uint64
	Connected   bool
	ConnectedAt time.Time // of the current or most recent stream

	SessionID     string
	ClaudeVersion string
	HelloAt       time.Time

	Busy             bool // a turn is running
	LastTurnComplete time.Time
	Usage            json.RawMessage // from the latest event that carried it

	Pending int // unacked commands
}

// State returns agent's current state; the zero State (with Agent set) if
// the hub knows nothing about it.
func (h *Hub) State(agent string) State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked(agent)
}

// WaitFor blocks until pred holds for agent's state and returns that state.
// pred runs without the hub lock held. Fails with ctx's error, or ErrClosed
// once the hub closes.
func (h *Hub) WaitFor(ctx context.Context, agent string, pred func(State) bool) (State, error) {
	for {
		h.mu.Lock()
		st, closed, changed := h.snapshotLocked(agent), h.closed, h.changed
		h.mu.Unlock()
		if pred(st) {
			return st, nil
		}
		if closed {
			return st, ErrClosed
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return st, ctx.Err()
		}
	}
}

func (h *Hub) snapshotLocked(agent string) State {
	var gen uint64
	if life, ok := h.lives[agent]; ok && !life.forgotten() {
		gen = life.gen
	}
	st, ok := h.agents[agent]
	if !ok {
		return State{Agent: agent, Gen: gen}
	}
	return State{
		Agent:            agent,
		Gen:              gen,
		Connected:        st.conn != nil,
		ConnectedAt:      st.connectedAt,
		SessionID:        st.sessionID,
		ClaudeVersion:    st.claudeVersion,
		HelloAt:          st.helloAt,
		Busy:             st.busy,
		LastTurnComplete: st.lastTurnComplete,
		Usage:            cloneRaw(st.usage),
		Pending:          len(st.outbox),
	}
}

func (h *Hub) applyEvent(agent, launch string, r Report) error {
	lock, err := h.eventLock(agent)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	// Checked under the event lock: the key may have been forgotten or
	// reopened for another launch since, and a dead launch's report must
	// not bring its state back or reach its successor.
	life, err := h.launchLocked(agent, launch)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	ev := h.recordLocked(agent, h.stateLocked(agent), r)
	ev.Gen = life.gen
	h.notifyLocked()
	subs := h.subs // AddSubscriber replaces, never mutates, the slice
	h.mu.Unlock()

	for _, sub := range subs {
		sub.OnBridgeEvent(ev)
	}
	return nil
}

// eventLock returns agent's event mutex, creating it on first use, or
// ErrForgotten for a tombstoned key (which must not grow a new one).
func (h *Hub) eventLock(agent string) (*sync.Mutex, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if life, ok := h.lives[agent]; ok && life.forgotten() {
		return nil, fmt.Errorf("%w: agent %s", ErrForgotten, agent)
	}
	lock, ok := h.eventLocks[agent]
	if !ok {
		lock = &sync.Mutex{}
		h.eventLocks[agent] = lock
	}
	return lock, nil
}

// recordLocked folds one hello/event report into st and returns the Event
// describing it.
func (h *Hub) recordLocked(agent string, st *agentState, r Report) Event {
	now := h.clock.Now()
	name := r.Name
	if r.Type == ReportHello {
		name = ReportHello
		// The mod's own word on a running turn wins: it covers a fresh
		// process resuming the same session after its predecessor died
		// mid-turn. Without it, a new session cannot have a turn running
		// yet, and the same session re-saying hello (a mod reload) keeps
		// whatever turn is on.
		switch {
		case r.Busy != nil:
			st.busy = *r.Busy
		case r.SessionID != st.sessionID:
			st.busy = false
		}
		st.sessionID, st.claudeVersion, st.helloAt = r.SessionID, r.ClaudeVersion, now
	}
	switch name {
	case EventTurnStart:
		st.busy = true
	case EventTurnComplete:
		st.busy = false
		st.lastTurnComplete = now
	case EventSessionEnd:
		st.busy = false
	}
	st.syncAckClock()
	if r.Usage != nil {
		st.usage = cloneRaw(r.Usage)
	}
	return Event{
		Agent:         agent,
		Name:          name,
		SessionID:     st.sessionID,
		ClaudeVersion: st.claudeVersion,
		Usage:         cloneRaw(r.Usage),
		Reason:        r.Reason,
		Prompt:        r.Prompt,
		Message:       r.Message,
		EventID:       r.EventID,
		Tokens:        cloneTokens(r.Tokens),
		Activity:      cloneOf(r.Activity),
		Attention:     cloneOf(r.Attention),
		Subagents:     cloneOf(r.Subagents),
		Compact:       cloneOf(r.Compact),
		At:            now,
	}
}

// cloneOf returns a copy of the flat struct p points to, or nil.
func cloneOf[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
