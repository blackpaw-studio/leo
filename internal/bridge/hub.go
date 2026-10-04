// Package bridge is the daemon side of the claude mod bridge: a per-agent
// connection registry, an ordered outbox of unacknowledged commands, and the
// turn state the mod reports back. It is transport-free — the daemon's HTTP
// handlers adapt a Stream to NDJSON and feed decoded reports to Apply — and
// takes its clock and id source by injection.
//
// Delivery is at-least-once: a command stays in the outbox until the mod
// acks it, and every (re)connection streams all unacked commands in order
// before new ones. The mod deduplicates by id, so ids must be unique across
// daemon restarts, not just within one process.
package bridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultAckTimeout bounds how long Send waits for the mod's ack.
	DefaultAckTimeout = 30 * time.Second
	// DefaultMaxPending caps one agent's unacked commands, so an agent whose
	// mod never connects cannot grow the outbox without bound.
	DefaultMaxPending = 256
)

var (
	ErrInvalidAgent = errors.New("invalid bridge agent")
	// ErrAckTimeout: Send gave up waiting; the command stays queued and is
	// redelivered on the next connection.
	ErrAckTimeout = errors.New("bridge ack timeout")
	// ErrRejected: the mod acked with ok:false; the command was dropped.
	ErrRejected       = errors.New("bridge command rejected")
	ErrOutboxFull     = errors.New("bridge outbox full")
	ErrStreamReplaced = errors.New("bridge stream replaced by a newer connection")
	ErrStreamClosed   = errors.New("bridge stream closed")
	ErrForgotten      = errors.New("bridge agent forgotten")
	ErrClosed         = errors.New("bridge closed")
)

// Clock is the hub's time source.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock returns the wall clock.
func SystemClock() Clock { return systemClock{} }

// NewCommandID returns a random 128-bit id. Randomness, not a counter, is what
// keeps ids unique across daemon restarts — a reused id would be skipped by
// the mod's dedup store as already done.
func NewCommandID() string { return "cmd-" + rand.Text() }

// Options configures a Hub. Zero values select the defaults.
type Options struct {
	Clock      Clock
	NewID      func() string
	AckTimeout time.Duration
	MaxPending int
	// Subscriber, if set, is handed every hello and turn/session event after
	// the hub state reflects it, outside the hub lock and in report order.
	// It may read the hub but must not call Apply (that would deadlock).
	Subscriber Subscriber
}

// Hub owns every agent's bridge state. It is safe for concurrent use.
type Hub struct {
	clock      Clock
	newID      func() string
	ackTimeout time.Duration
	maxPending int
	sub        Subscriber

	// eventMu serializes hello/event application with its subscriber call,
	// so subscribers observe events in the same order the state took them.
	eventMu sync.Mutex

	mu      sync.Mutex
	agents  map[string]*agentState
	seq     uint64
	closed  bool
	changed chan struct{} // closed and replaced on every state change
}

type agentState struct {
	outbox      []*pending // ascending seq
	conn        *conn
	connectedAt time.Time

	sessionID        string
	claudeVersion    string
	helloAt          time.Time
	busy             bool
	lastTurnComplete time.Time
	usage            json.RawMessage
}

// pending is one unacked command. result receives exactly one resolution
// (ack, rejection, forget, close), always under Hub.mu.
type pending struct {
	seq    uint64
	cmd    Command
	result chan error
}

// conn is one stream registration. nextSeq is its delivery cursor: a fresh
// conn starts at zero, which is what makes reconnects redeliver.
type conn struct {
	nextSeq uint64
	wake    chan struct{}
	done    chan struct{}
	err     error // why done was closed; set under Hub.mu
}

// New builds a Hub.
func New(opts Options) *Hub {
	h := &Hub{
		clock:      opts.Clock,
		newID:      opts.NewID,
		ackTimeout: opts.AckTimeout,
		maxPending: opts.MaxPending,
		sub:        opts.Subscriber,
		agents:     map[string]*agentState{},
		changed:    make(chan struct{}),
	}
	if h.clock == nil {
		h.clock = SystemClock()
	}
	if h.newID == nil {
		h.newID = NewCommandID
	}
	if h.ackTimeout <= 0 {
		h.ackTimeout = DefaultAckTimeout
	}
	if h.maxPending <= 0 {
		h.maxPending = DefaultMaxPending
	}
	return h
}

// Enqueue queues cmd for agent and returns its id without waiting for the
// ack. Use it for commands queued before the mod connects (the opening
// prompt); everything else should Send.
func (h *Hub) Enqueue(agent string, cmd Command) (string, error) {
	p, err := h.enqueue(agent, cmd)
	if err != nil {
		return "", err
	}
	return p.cmd.ID, nil
}

// Send queues cmd and waits for the mod's ack. It returns nil on ok:true and
// an ErrRejected error on ok:false (the command is dropped either way). On
// ack timeout or ctx cancellation the command stays queued for redelivery,
// so the caller must not retry it by another route.
func (h *Hub) Send(ctx context.Context, agent string, cmd Command) error {
	p, err := h.enqueue(agent, cmd)
	if err != nil {
		return err
	}
	timeout := h.clock.After(h.ackTimeout)
	select {
	case err := <-p.result:
		return err
	case <-timeout:
		return h.abandon(p, fmt.Errorf("%w: agent %s command %s after %s", ErrAckTimeout, agent, p.cmd.ID, h.ackTimeout))
	case <-ctx.Done():
		return h.abandon(p, fmt.Errorf("bridge send to %s (command %s): %w", agent, p.cmd.ID, ctx.Err()))
	}
}

// abandon stops waiting on p. A resolution that landed before the hub lock
// was taken wins over cause: the command did settle.
func (h *Hub) abandon(p *pending, cause error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case err := <-p.result:
		return err
	default:
		return cause
	}
}

func (h *Hub) enqueue(agent string, cmd Command) (*pending, error) {
	if agent == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	cmd.ID = h.newID()
	if cmd.ID == "" {
		return nil, fmt.Errorf("%w: id generator returned an empty id", ErrInvalidCommand)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	st := h.stateLocked(agent)
	if len(st.outbox) >= h.maxPending {
		return nil, fmt.Errorf("%w: agent %s has %d unacked commands", ErrOutboxFull, agent, len(st.outbox))
	}
	if findPending(st.outbox, cmd.ID) >= 0 {
		return nil, fmt.Errorf("%w: duplicate id %s", ErrInvalidCommand, cmd.ID)
	}
	h.seq++
	p := &pending{seq: h.seq, cmd: cmd, result: make(chan error, 1)}
	st.outbox = append(st.outbox, p)
	if st.conn != nil {
		st.conn.signal()
	}
	h.notifyLocked()
	return p, nil
}

// Apply records one decoded report from agent's mod. Acks for unknown ids
// (already settled, or another daemon's) are ignored.
func (h *Hub) Apply(agent string, r Report) error {
	if agent == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	switch r.Type {
	case ReportAck:
		return h.applyAck(agent, r)
	case ReportHello, ReportEvent:
		return h.applyEvent(agent, r)
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidReport, r.Type)
	}
}

func (h *Hub) applyAck(agent string, r Report) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.ackLocked(agent, r.ID, r.OK, r.Error)
	return nil
}

// ackLocked settles agent's command id, if it is still queued.
func (h *Hub) ackLocked(agent, id string, ok bool, msg string) {
	st, found := h.agents[agent]
	if !found {
		return
	}
	i := findPending(st.outbox, id)
	if i < 0 {
		return
	}
	p := st.outbox[i]
	st.outbox = without(st.outbox, i)
	var outcome error
	if !ok {
		outcome = rejection(agent, id, msg)
	}
	p.result <- outcome
	h.notifyLocked()
}

func rejection(agent, id, msg string) error {
	if msg == "" {
		msg = "no reason given"
	}
	return fmt.Errorf("%w: agent %s command %s: %s", ErrRejected, agent, id, msg)
}

// Connect registers a new stream for agent, ending any previous one with
// ErrStreamReplaced. The new stream starts with every unacked command.
func (h *Hub) Connect(agent string) (*Stream, error) {
	if agent == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	st := h.stateLocked(agent)
	if st.conn != nil {
		st.conn.closeLocked(ErrStreamReplaced)
	}
	c := &conn{wake: make(chan struct{}, 1), done: make(chan struct{})}
	st.conn = c
	st.connectedAt = h.clock.Now()
	h.notifyLocked()
	return &Stream{hub: h, agent: agent, conn: c}, nil
}

// Connected reports whether agent has a live stream.
func (h *Hub) Connected(agent string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.agents[agent]
	return ok && st.conn != nil
}

// Forget drops everything known about agent: its stream ends and every
// unacked command is discarded, waking its senders with ErrForgotten. Use it
// when the agent is stopped, reset or relaunched without the mod.
func (h *Hub) Forget(agent string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.agents[agent]
	if !ok {
		return
	}
	h.dropLocked(agent, st, ErrForgotten)
	delete(h.agents, agent)
	h.notifyLocked()
}

// Close ends every stream, fails every waiting Send with ErrClosed and
// refuses further work. Idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for agent, st := range h.agents {
		h.dropLocked(agent, st, ErrClosed)
	}
	h.agents = map[string]*agentState{}
	h.notifyLocked()
}

func (h *Hub) dropLocked(agent string, st *agentState, cause error) {
	if st.conn != nil {
		st.conn.closeLocked(cause)
		st.conn = nil
	}
	for _, p := range st.outbox {
		p.result <- fmt.Errorf("%w: agent %s command %s was not delivered", cause, agent, p.cmd.ID)
	}
	st.outbox = nil
}

func (h *Hub) stateLocked(agent string) *agentState {
	st, ok := h.agents[agent]
	if !ok {
		st = &agentState{}
		h.agents[agent] = st
	}
	return st
}

// notifyLocked wakes every WaitFor.
func (h *Hub) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// nextFor returns the next command for c, or a channel to wait on when none
// is ready. A closed conn reports why it closed.
func (h *Hub) nextFor(agent string, c *conn) (Command, <-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.err != nil {
		return Command{}, nil, c.err
	}
	st := h.agents[agent] // non-nil: removing an agent closes its conn first
	for _, p := range st.outbox {
		if p.seq >= c.nextSeq {
			c.nextSeq = p.seq + 1
			return p.cmd, nil, nil
		}
	}
	return Command{}, c.wake, nil
}

func (h *Hub) release(agent string, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.agents[agent]; ok && st.conn == c {
		st.conn = nil
		h.notifyLocked()
	}
	c.closeLocked(ErrStreamClosed)
}

func (c *conn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) closeLocked(err error) {
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
}

func findPending(outbox []*pending, id string) int {
	for i, p := range outbox {
		if p.cmd.ID == id {
			return i
		}
	}
	return -1
}

// without returns a new slice lacking element i.
func without(outbox []*pending, i int) []*pending {
	out := make([]*pending, 0, len(outbox)-1)
	out = append(out, outbox[:i]...)
	return append(out, outbox[i+1:]...)
}

// Stream is one connection's view of an agent's commands.
type Stream struct {
	hub   *Hub
	agent string
	conn  *conn
}

// Next returns the next command to write, blocking until one is queued. It
// fails with ErrStreamReplaced, ErrStreamClosed, ErrForgotten or ErrClosed
// once the stream is over, or with ctx's error. A ready command is returned
// even if ctx is already done.
func (s *Stream) Next(ctx context.Context) (Command, error) {
	for {
		cmd, wait, err := s.hub.nextFor(s.agent, s.conn)
		if err != nil || wait == nil {
			return cmd, err
		}
		select {
		case <-wait:
		case <-s.conn.done:
		case <-ctx.Done():
			return Command{}, ctx.Err()
		}
	}
}

// Close unregisters the stream if it is still the agent's live one. Safe to
// call more than once, and after replacement.
func (s *Stream) Close() { s.hub.release(s.agent, s.conn) }
