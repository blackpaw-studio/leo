package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// OpState marks a state line on the stream: a full snapshot of what the mod
// shows and enforces, not a command. It has no id, is never acked or kept in
// an outbox, and the latest one wins.
const OpState = "state"

var (
	// ErrRequestUnavailable: no request handler is wired (the daemon serves
	// no dispatcher), so a mod's request cannot be served.
	ErrRequestUnavailable = errors.New("bridge requests are not served")
	// ErrRequestDenied: the request names something its agent may not act
	// on, such as another agent's dispatch.
	ErrRequestDenied = errors.New("bridge request denied")
)

// StateSnapshot is everything the mod draws and enforces from: the
// delegation policy and the caller's own dispatches.
type StateSnapshot struct {
	Delegation DelegationState `json:"delegation"`
	Dispatches []DispatchState `json:"dispatches"`
}

// DelegationState is the delegation policy for one agent. Section is the
// text the mod adds to the system prompt while Enabled; HideAgents are the
// native agent types it hides then.
type DelegationState struct {
	Enabled    bool     `json:"enabled"`
	Section    string   `json:"section"`
	HideAgents []string `json:"hide_agents"`
}

// DispatchState is one dispatch as the mod's roster band shows it.
// ActiveSeconds is the working time when the snapshot was taken; the mod
// adds its own clock to it while Status is running.
type DispatchState struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Template      string   `json:"template"`
	Model         string   `json:"model"`
	Effort        string   `json:"effort,omitempty"`
	Status        string   `json:"status"`
	Stalled       bool     `json:"stalled"`
	ActiveSeconds float64  `json:"active_seconds"`
	TokensIn      *int64   `json:"tokens_in,omitempty"`
	TokensOut     *int64   `json:"tokens_out,omitempty"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
}

// DispatchRunning is the status whose working time keeps counting.
const DispatchRunning = "running"

// agedAt returns s as of elapsed after it was taken: each running
// dispatch's working time moved on by elapsed. s itself is left alone.
func (s StateSnapshot) agedAt(elapsed time.Duration) StateSnapshot {
	if elapsed <= 0 {
		return s
	}
	dispatches := make([]DispatchState, len(s.Dispatches))
	for i, d := range s.Dispatches {
		if d.Status == DispatchRunning {
			d.ActiveSeconds += elapsed.Seconds()
		}
		dispatches[i] = d
	}
	s.Dispatches = dispatches
	return s
}

// stateLine encodes s as one NDJSON line: {"op":"state", ...s}.
func stateLine(s StateSnapshot) ([]byte, error) {
	if s.Dispatches == nil {
		s.Dispatches = []DispatchState{}
	}
	if s.Delegation.HideAgents == nil {
		s.Delegation.HideAgents = []string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	wire := struct {
		Op string `json:"op"`
		StateSnapshot
	}{OpState, s}
	if err := enc.Encode(wire); err != nil {
		return nil, fmt.Errorf("encoding bridge state: %w", err)
	}
	return buf.Bytes(), nil
}

// SetState makes s agent's state: every stream of the key's current
// generation gets it next (ahead of queued commands), a later reconnect
// starts with it, and a newer SetState replaces it before it is sent. A
// line is encoded when it is sent, running dispatches aged by the time
// since SetState, so a replay never sets the mod's clock back. It fails
// with ErrNotOpen for a key no launch opened.
func (h *Hub) SetState(agent string, s StateSnapshot) error {
	if _, err := stateLine(s); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if _, err := h.lifeLocked(agent); err != nil {
		return err
	}
	st := h.stateLocked(agent)
	st.state = &s
	st.stateAt = h.clock.Now()
	st.stateSeq++
	if st.conn != nil {
		st.conn.signal()
	}
	return nil
}

// ConnectedKeys returns the keys with a live stream, sorted.
func (h *Hub) ConnectedKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.agents))
	for agent, st := range h.agents {
		if st.conn != nil {
			keys = append(keys, agent)
		}
	}
	sort.Strings(keys)
	return keys
}

// RequestHandler serves a mod's request: op on dispatchID, asked by the mod
// of agent's current launch. It returns ErrRequestDenied (wrapped) for a
// target agent may not act on.
type RequestHandler func(agent, op, dispatchID string) error

// SetRequestHandler wires fn to serve requests; nil unwires it.
func (h *Hub) SetRequestHandler(fn RequestHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = fn
}

// applyRequest checks the request comes from agent's current launch, then
// serves it outside the hub lock.
func (h *Hub) applyRequest(agent, launch string, r Report) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	if _, err := h.launchLocked(agent, launch); err != nil {
		h.mu.Unlock()
		return err
	}
	serve := h.requests
	h.mu.Unlock()
	if serve == nil {
		return ErrRequestUnavailable
	}
	return serve(agent, r.Op, r.DispatchID)
}

// NextLine returns the next line to write to the mod: the agent's latest
// state if this stream has not had it, else the next command. It blocks and
// fails as Next does.
func (s *Stream) NextLine(ctx context.Context) ([]byte, error) {
	for {
		line, cmd, wait, err := s.hub.nextLineFor(s.agent, s.conn)
		if err != nil {
			return nil, err
		}
		if line != nil {
			return line, nil
		}
		if wait == nil {
			return cmd.MarshalLine()
		}
		select {
		case <-wait:
		case <-s.conn.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// nextLineFor is nextFor with the agent's state first: a state line c has
// not sent, or else what nextFor gives.
func (h *Hub) nextLineFor(agent string, c *conn) (line []byte, cmd Command, wait <-chan struct{}, err error) {
	h.mu.Lock()
	var state *StateSnapshot
	var age time.Duration
	if c.err == nil {
		if st := h.agents[agent]; st != nil && st.stateSeq > c.stateSeq {
			c.stateSeq = st.stateSeq
			state, age = st.state, h.clock.Now().Sub(st.stateAt)
		}
	}
	h.mu.Unlock()
	if state != nil {
		line, err = stateLine(state.agedAt(age))
		return line, Command{}, nil, err
	}
	cmd, wait, err = h.nextFor(agent, c)
	return nil, cmd, wait, err
}
