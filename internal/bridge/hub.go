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
	// DefaultSlowAckTimeout bounds compact and clear: compaction summarizes
	// the conversation with a model call and can legitimately run for minutes.
	DefaultSlowAckTimeout = 5 * time.Minute
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
	// ErrNotOpen: no launch opened the key. Nothing can be queued for it;
	// a mod naming it may be a surviving session a restarting daemon has not
	// adopted yet, and should retry (see AwaitAdoption).
	ErrNotOpen = errors.New("bridge agent not open")
	// ErrStaleLaunch: a mod's stream or report named a launch other than
	// its key's current one (a predecessor's), or a key the daemon is not
	// adopting. It is refused for good; a report it carried is moot.
	ErrStaleLaunch   = errors.New("bridge launch is not current")
	ErrInvalidLaunch = errors.New("invalid bridge launch")
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
	Clock Clock
	NewID func() string
	// AckTimeout is how long an idle agent has to ack a command (default
	// 30s); time spent in a running turn does not count. See Send.
	AckTimeout time.Duration
	// SlowAckTimeout replaces AckTimeout for compact and clear (default 5m).
	SlowAckTimeout time.Duration
	MaxPending     int
	// Subscriber, if set, is handed every hello and turn/session event after
	// the hub state reflects it, outside the hub lock and in each agent's
	// report order. It may read the hub but must not call Apply for the same
	// agent (that would deadlock) and should not block: a slow call holds up
	// that agent's later reports.
	Subscriber Subscriber
}

// Hub owns every agent's bridge state. It is safe for concurrent use.
type Hub struct {
	clock          Clock
	newID          func() string
	ackTimeout     time.Duration
	slowAckTimeout time.Duration
	maxPending     int

	mu     sync.Mutex
	subs   []Subscriber // Options.Subscriber, then AddSubscriber's, in order
	agents map[string]*agentState
	// eventLocks holds one mutex per agent, serializing that agent's
	// hello/event application with its subscriber call, so subscribers see
	// an agent's events in the order its state took them — without one slow
	// subscriber call stalling every other agent's reports. Guarded by mu.
	eventLocks map[string]*sync.Mutex
	seq        uint64
	// lives holds each key's generation, and the tombstone a Forget leaves
	// until the next Open (see generation.go). Guarded by mu.
	lives  map[string]*keyLife
	genSeq uint64
	// isAdoptionSettled is set once the daemon has said which keys it is
	// adopting (awaiting holds those not opened yet): see AwaitAdoption.
	// Guarded by mu.
	isAdoptionSettled bool
	awaiting          map[string]bool
	closed            bool
	// requests serves mods' request reports (see SetRequestHandler).
	requests RequestHandler
	changed  chan struct{} // closed and replaced on every state change
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

	// stateLine is the agent's latest state line (see SetState), and
	// stateSeq counts its replacements, so each stream sends the newest once.
	stateLine []byte
	stateSeq  uint64

	// sealed is set once the mod refuses a gate (see EnqueueGate): nothing
	// more streams in this generation.
	sealed bool

	// ackClockPaused is busy && connected: the mod only acks a queued
	// command once Claude is idle, so ack clocks stand still meanwhile.
	// idleEpoch counts paused→running flips so a waiting Send restarts its
	// clock on each, even one it did not see happen. See syncAckClock.
	ackClockPaused bool
	idleEpoch      uint64
}

// syncAckClock recomputes the ack-clock pause after any change to busy or
// conn. Busy only counts while connected: a mod that vanished mid-turn
// will never report the turn's end.
func (st *agentState) syncAckClock() {
	paused := st.busy && st.conn != nil
	if st.ackClockPaused && !paused {
		st.idleEpoch++
	}
	st.ackClockPaused = paused
}

// pending is one unacked command. It is resolved exactly once (ack,
// rejection, forget, close, or an abandoned non-deliver), always under
// Hub.mu: err is set, then done is closed, so any number of waiters can
// read err once done is.
type pending struct {
	seq  uint64
	cmd  Command
	done chan struct{}
	err  error
	// ackedIn is the session the command was acked ok in (see
	// Ticket.AckedIn); set with err.
	ackedIn string
	// callerID: the caller named the command (see EnqueueTo).
	callerID bool
	queueAs
}

// queueAs is how a command is queued.
type queueAs struct {
	// gate: nothing behind it streams until it is acked ok (see
	// EnqueueGate).
	gate bool
	// opening: its generation's opening, which takes no slot under the
	// agent's cap (see EnqueueOpening).
	opening bool
}

func newPending(seq uint64, cmd Command, callerID bool, as queueAs) *pending {
	return &pending{seq: seq, cmd: cmd, done: make(chan struct{}), callerID: callerID, queueAs: as}
}

// openingAndLoad returns outbox's opening (nil without one) and how many
// of its commands count against the cap: every one but the opening.
func openingAndLoad(outbox []*pending) (opening *pending, load int) {
	for _, p := range outbox {
		if p.opening {
			opening = p
			continue
		}
		load++
	}
	return opening, load
}

// resolveLocked settles p with err; later calls are no-ops.
func (p *pending) resolveLocked(err error) {
	select {
	case <-p.done:
	default:
		p.err = err
		close(p.done)
	}
}

// conn is one stream registration. nextSeq is its delivery cursor: a fresh
// conn starts at zero, which is what makes reconnects redeliver.
type conn struct {
	nextSeq uint64
	// stateSeq is the agent's stateSeq this stream last sent.
	stateSeq uint64
	wake     chan struct{}
	done     chan struct{}
	err      error // why done was closed; set under Hub.mu
}

// New builds a Hub.
func New(opts Options) *Hub {
	h := &Hub{
		clock:          opts.Clock,
		newID:          opts.NewID,
		ackTimeout:     opts.AckTimeout,
		slowAckTimeout: opts.SlowAckTimeout,
		maxPending:     opts.MaxPending,
		agents:         map[string]*agentState{},
		eventLocks:     map[string]*sync.Mutex{},
		lives:          map[string]*keyLife{},
		awaiting:       map[string]bool{},
		changed:        make(chan struct{}),
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
	if h.slowAckTimeout <= 0 {
		h.slowAckTimeout = DefaultSlowAckTimeout
	}
	if opts.Subscriber != nil {
		h.subs = []Subscriber{opts.Subscriber}
	}
	if h.maxPending <= 0 {
		h.maxPending = DefaultMaxPending
	}
	return h
}

// Enqueue queues cmd for agent's current generation and returns its id
// without waiting for the ack. It fails with ErrNotOpen for a key no launch
// opened. Launch code queues with EnqueueTo instead, pinned to its own
// generation.
func (h *Hub) Enqueue(agent string, cmd Command) (string, error) {
	p, err := h.enqueue(agent, nil, cmd, queueAs{})
	if err != nil {
		return "", err
	}
	return p.cmd.ID, nil
}

// Send queues cmd and waits for the mod's ack. It returns nil on ok:true and
// an ErrRejected error on ok:false (the command is dropped either way).
//
// On ack timeout or ctx cancellation a deliver stays queued for redelivery,
// so the caller must not retry it by another route. Any other op (interrupt,
// clear, compact) is dropped from the outbox instead: acting on it later,
// once its sender has given up, would interrupt or wipe unrelated work. One
// already written to the stream may still run; it is just never resent.
//
// The ack timeout is turn-aware and queue-aware. The mod runs deliver,
// compact and clear one at a time, in order, and acks a deliver only once
// Claude is idle. So a command's clock stands still while an earlier one of
// those is still unacked, and (for every op but interrupt) while the agent's
// stream is up and a turn is running; each return to idle restarts it in
// full. Send therefore fails only after a full timeout of being free to run
// without an ack — AckTimeout, or SlowAckTimeout for compact and clear. A
// long turn can hold Send for as long as it runs; bound that with ctx.
// Interrupt is the exception — the mod runs it at once, mid-turn and ahead
// of the queue — so its clock runs from the start regardless.
func (h *Hub) Send(ctx context.Context, agent string, cmd Command) error {
	p, err := h.enqueue(agent, nil, cmd, queueAs{})
	if err != nil {
		return err
	}
	return h.await(ctx, agent, p)
}

func (h *Hub) await(ctx context.Context, agent string, p *pending) error {
	limit := h.ackTimeoutFor(p.cmd.Op)
	var (
		timeout    <-chan time.Time
		armedEpoch uint64
	)
	for {
		paused, epoch, changed := h.ackClock(agent, p)
		switch {
		case paused:
			timeout = nil
		case timeout == nil || epoch != armedEpoch:
			timeout, armedEpoch = h.clock.After(limit), epoch
		}
		select {
		case <-p.done:
			return p.err
		case <-timeout:
			return h.abandon(agent, p, fmt.Errorf("%w: agent %s command %s: no ack within %s of it being free to run",
				ErrAckTimeout, agent, p.cmd.ID, limit))
		case <-changed:
		case <-ctx.Done():
			return h.abandon(agent, p, fmt.Errorf("bridge send to %s (command %s): %w", agent, p.cmd.ID, ctx.Err()))
		}
	}
}

// ackTimeoutFor is how long op has to be acked once it is free to run.
func (h *Hub) ackTimeoutFor(op string) time.Duration {
	if op == OpCompact || op == OpClear {
		return h.slowAckTimeout
	}
	return h.ackTimeout
}

// isSerial reports whether the mod runs op on its in-order command chain.
// Interrupt is the one op it runs at once, ahead of the queue.
func isSerial(op string) bool { return op != OpInterrupt }

// ackClock reports whether p's ack clock is paused, the epoch its running
// clock was armed under (a change means "restart in full"), and the channel
// that closes on the next state change. p's clock is paused while an earlier
// serial command is still unacked and, unless p is an interrupt, while the
// agent is busy on a live stream.
func (h *Hub) ackClock(agent string, p *pending) (paused bool, epoch uint64, changed <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.agents[agent]
	if !ok {
		return false, 0, h.changed
	}
	if isSerial(p.cmd.Op) && queuedBehindLocked(st.outbox, p) {
		return true, 0, h.changed
	}
	if p.cmd.Op == OpInterrupt {
		return false, 0, h.changed
	}
	return st.ackClockPaused, st.idleEpoch, h.changed
}

// queuedBehindLocked reports whether an earlier serial command than p is
// still unacked in outbox.
func queuedBehindLocked(outbox []*pending, p *pending) bool {
	for _, q := range outbox {
		if q.seq >= p.seq {
			return false
		}
		if isSerial(q.cmd.Op) {
			return true
		}
	}
	return false
}

// abandon stops waiting on p. A resolution that landed before the hub lock
// was taken wins over cause: the command did settle. Otherwise a deliver
// stays queued for redelivery and any other op is dropped (see Send),
// settling with cause for anyone else watching it.
func (h *Hub) abandon(agent string, p *pending, cause error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-p.done:
		return p.err
	default:
	}
	if p.cmd.Op != OpDeliver {
		if st, ok := h.agents[agent]; ok {
			if i := findPending(st.outbox, p.cmd.ID); i >= 0 {
				st.outbox = without(st.outbox, i)
				h.notifyLocked()
			}
		}
		p.resolveLocked(cause)
	}
	return cause
}

// enqueue queues cmd for agent: for generation *gen when gen is non-nil,
// else for whatever generation is current; as a gate and/or the opening as
// as says (see EnqueueGate, EnqueueOpening). A caller-set cmd.ID is kept
// (see EnqueueTo); otherwise the hub assigns one.
func (h *Hub) enqueue(agent string, gen *uint64, cmd Command, as queueAs) (*pending, error) {
	if agent == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	callerID := cmd.ID != ""
	if !callerID {
		cmd.ID = h.newID()
	}
	if cmd.ID == "" {
		return nil, fmt.Errorf("%w: id generator returned an empty id", ErrInvalidCommand)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	life, err := h.lifeLocked(agent)
	if err != nil {
		return nil, err
	}
	if gen != nil && life.gen != *gen {
		return nil, fmt.Errorf("%w: agent %s was relaunched", ErrForgotten, agent)
	}
	st := h.stateLocked(agent)
	if i := findPending(st.outbox, cmd.ID); i >= 0 {
		if q := st.outbox[i]; callerID && q.callerID && q.cmd == cmd && q.queueAs == as {
			return q, nil
		}
		return nil, fmt.Errorf("%w: duplicate id %s", ErrInvalidCommand, cmd.ID)
	}
	opening, load := openingAndLoad(st.outbox)
	switch {
	case as.opening && opening != nil:
		return nil, fmt.Errorf("%w: agent %s has opening %s queued already", ErrInvalidCommand, agent, opening.cmd.ID)
	case !as.opening && load >= h.maxPending:
		return nil, fmt.Errorf("%w: agent %s has %d unacked commands", ErrOutboxFull, agent, load)
	}
	h.seq++
	p := newPending(h.seq, cmd, callerID, as)
	st.outbox = append(st.outbox, p)
	if st.conn != nil {
		st.conn.signal()
	}
	h.notifyLocked()
	return p, nil
}

// Apply records one decoded report from the mod of agent's launch. A launch
// other than the key's current one is refused with ErrStaleLaunch (see
// Open), so a dying predecessor's late report changes nothing. Acks for
// unknown ids (already settled, or another daemon's) are ignored.
func (h *Hub) Apply(agent, launch string, r Report) error {
	if agent == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	if err := ValidateLaunch(launch); err != nil {
		return err
	}
	switch r.Type {
	case ReportAck:
		return h.applyAck(agent, launch, r)
	case ReportHello, ReportEvent:
		return h.applyEvent(agent, launch, r)
	case ReportRequest:
		return h.applyRequest(agent, launch, r)
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidReport, r.Type)
	}
}

func (h *Hub) applyAck(agent, launch string, r Report) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if _, err := h.launchLocked(agent, launch); err != nil {
		return err
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
	if !ok {
		p.resolveLocked(rejection(agent, id, msg))
		st.sealed = st.sealed || p.gate
	} else {
		p.ackedIn = st.sessionID
		p.resolveLocked(nil)
	}
	if st.conn != nil {
		st.conn.signal() // a gate's ack frees what it held back
	}
	h.notifyLocked()
}

func rejection(agent, id, msg string) error {
	if msg == "" {
		msg = "no reason given"
	}
	return fmt.Errorf("%w: agent %s command %s: %s", ErrRejected, agent, id, msg)
}

// Connect registers a new stream for the mod of agent's launch, ending any
// previous one with ErrStreamReplaced. The new stream starts with every
// unacked command. A launch other than the key's current one is refused
// with ErrStaleLaunch (see Open).
func (h *Hub) Connect(agent, launch string) (*Stream, error) {
	if agent == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	if err := ValidateLaunch(launch); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if _, err := h.launchLocked(agent, launch); err != nil {
		return nil, err
	}
	st := h.stateLocked(agent)
	if st.conn != nil {
		st.conn.closeLocked(ErrStreamReplaced)
	}
	c := &conn{wake: make(chan struct{}, 1), done: make(chan struct{})}
	st.conn = c
	st.connectedAt = h.clock.Now()
	st.syncAckClock()
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
// unacked command is discarded, waking its senders with ErrForgotten. The
// key is then tombstoned: commands, connections and reports for it are
// refused with ErrForgotten until the next Open. Use it whenever the launch
// behind the key is over.
func (h *Hub) Forget(agent string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.forgetLocked(agent)
}

// ForgetUnlessConnected forgets agent, as Forget does, only if its stream is
// not connected, reporting whether it did. The check and the forget happen
// under one lock, so a bridge that connects at the last moment is kept.
func (h *Hub) ForgetUnlessConnected(agent string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.agents[agent]; ok && st.conn != nil {
		return false
	}
	h.forgetLocked(agent)
	return true
}

func (h *Hub) forgetLocked(agent string) {
	delete(h.eventLocks, agent)
	if st, ok := h.agents[agent]; ok {
		h.dropLocked(agent, st, ErrForgotten)
		delete(h.agents, agent)
	}
	h.tombstoneLocked(agent)
	h.notifyLocked()
}

// AddSubscriber adds sub, alongside Options.Subscriber, for every hello and
// turn/session event applied from now on, under the same contract. For
// subscribers built after the hub, such as the dispatch adapter that lives
// with the web server. A nil sub is ignored.
func (h *Hub) AddSubscriber(sub Subscriber) {
	if sub == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs = append(append([]Subscriber(nil), h.subs...), sub)
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
		p.resolveLocked(fmt.Errorf("%w: agent %s command %s was not delivered", cause, agent, p.cmd.ID))
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
// is ready. A closed conn reports why it closed. A gate c has handed out
// and the mod has not acked holds back everything after it, and a sealed
// generation hands out nothing more (see EnqueueGate).
func (h *Hub) nextFor(agent string, c *conn) (Command, <-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.err != nil {
		return Command{}, nil, c.err
	}
	st := h.agents[agent] // non-nil: removing an agent closes its conn first
	if st.sealed {
		return Command{}, c.wake, nil
	}
	for _, p := range st.outbox {
		if p.seq >= c.nextSeq {
			c.nextSeq = p.seq + 1
			return p.cmd, nil, nil
		}
		if p.gate {
			break
		}
	}
	return Command{}, c.wake, nil
}

func (h *Hub) release(agent string, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.agents[agent]; ok && st.conn == c {
		st.conn = nil
		st.syncAckClock()
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
