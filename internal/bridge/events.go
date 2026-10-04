package bridge

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Event is a hello or turn/session event as seen by a Subscriber. Name is
// ReportHello or one of the Event* names. SessionID and ClaudeVersion come
// from the agent's latest hello (the event's own, for a hello).
type Event struct {
	Agent         string
	Name          string
	SessionID     string
	ClaudeVersion string
	Usage         json.RawMessage
	Reason        string
	Prompt        string // turn.start
	Message       string // turn.complete
	EventID       string // stable across retries of one event; may be empty
	At            time.Time
}

// Subscriber is told about every hello and turn/session event — the seam
// later phases use to feed consult reports and idle-suspend.
type Subscriber interface {
	OnBridgeEvent(Event)
}

// SubscriberFunc adapts a function to Subscriber.
type SubscriberFunc func(Event)

// OnBridgeEvent calls f.
func (f SubscriberFunc) OnBridgeEvent(ev Event) { f(ev) }

// State is a point-in-time copy of one agent's bridge state.
type State struct {
	Agent       string
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
	st, ok := h.agents[agent]
	if !ok {
		return State{Agent: agent}
	}
	return State{
		Agent:            agent,
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

func (h *Hub) applyEvent(agent string, r Report) error {
	lock := h.eventLock(agent)
	lock.Lock()
	defer lock.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	ev := h.recordLocked(agent, h.stateLocked(agent), r)
	h.notifyLocked()
	h.mu.Unlock()

	if h.sub != nil {
		h.sub.OnBridgeEvent(ev)
	}
	return nil
}

// eventLock returns agent's event mutex, creating it on first use.
func (h *Hub) eventLock(agent string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	lock, ok := h.eventLocks[agent]
	if !ok {
		lock = &sync.Mutex{}
		h.eventLocks[agent] = lock
	}
	return lock
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
		At:            now,
	}
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
