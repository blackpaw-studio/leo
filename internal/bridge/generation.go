package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TombstoneTTL is how long a forgotten key refuses work before it is
// treated as never seen. It only has to outlive the last reports and
// reconnects of a launch that is already dead (the mod gives up retrying a
// report within about a minute).
const TombstoneTTL = time.Hour

// maxLaunchLen bounds a launch token. leo mints 26-character ones.
const maxLaunchLen = 64

// Target is one launch generation of an agent's bridge key. A launch that
// reuses a key (a relaunch of the same agent) opens a new generation, so a
// command routed to the old launch can be refused instead of reaching the
// new one.
type Target struct {
	Key string
	Gen uint64
}

// keyLife is a key's current generation, the launch it is bound to, and
// whether it is tombstoned.
type keyLife struct {
	gen uint64
	// launch is the token (LEO_BRIDGE_LAUNCH) of the claude process the
	// generation belongs to: only its mod may connect or report. Empty for
	// a tombstone left on a key nobody opened.
	launch string
	// forgottenAt is when the key was forgotten; zero while it is live.
	forgottenAt time.Time
}

func (l *keyLife) forgotten() bool { return !l.forgottenAt.IsZero() }

// Ticket is a command queued by EnqueueTo. Holding one never keeps the
// command alive or drops it; it only observes how it settles.
type Ticket struct {
	ID string
	p  *pending
}

// Done is closed once the command settles: acked, rejected, forgotten or
// dropped by Close.
func (t *Ticket) Done() <-chan struct{} { return t.p.done }

// AckedIn is the session the mod's latest hello named when it acked the
// command ok: the conversation a deliver landed in. "" until then, or when
// the mod never said hello.
func (t *Ticket) AckedIn() string {
	select {
	case <-t.p.done:
		return t.p.ackedIn
	default:
		return ""
	}
}

// Err is how the command settled: nil once acked ok, else why not. Only
// meaningful after Done is closed.
func (t *Ticket) Err() error {
	select {
	case <-t.p.done:
		return t.p.err
	default:
		return nil
	}
}

// Wait blocks until the command settles and returns Err, or returns ctx's
// error first. Giving up leaves the command queued.
func (t *Ticket) Wait(ctx context.Context) error {
	select {
	case <-t.p.done:
		return t.p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Open starts a new generation of agent's key, bound to launch: the token
// (LEO_BRIDGE_LAUNCH) of the one claude process whose mod may connect and
// report under it. It lifts the tombstone a
// Forget left. Whatever generation the key had is over, as if forgotten:
// its stream ends, its unacked commands fail with ErrForgotten and its turn
// state is dropped, so a predecessor's mod cannot keep the key, and a
// predecessor's ForgetGen cannot end this generation. A launch about to
// start opens a fresh token; a restarted daemon adopting a surviving
// session opens the token that session was launched with.
func (h *Hub) Open(agent, launch string) (Target, error) {
	if agent == "" {
		return Target{}, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	if err := ValidateLaunch(launch); err != nil {
		return Target{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return Target{}, ErrClosed
	}
	h.pruneTombstonesLocked()
	if st, ok := h.agents[agent]; ok {
		h.dropLocked(agent, st, ErrForgotten)
		delete(h.agents, agent)
	}
	life := &keyLife{gen: h.nextGenLocked(), launch: launch}
	h.lives[agent] = life
	h.notifyLocked()
	return Target{Key: agent, Gen: life.gen}, nil
}

// ValidateLaunch checks a launch token: non-empty, at most 64 letters,
// digits, '-' or '_'. It travels in a URL query and a log line.
func ValidateLaunch(launch string) error {
	if launch == "" || len(launch) > maxLaunchLen {
		return fmt.Errorf("%w: %q", ErrInvalidLaunch, launch)
	}
	for _, r := range launch {
		isWord := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !isWord {
			return fmt.Errorf("%w: %q", ErrInvalidLaunch, launch)
		}
	}
	return nil
}

// Live returns agent's current generation while its mod is connected.
func (h *Hub) Live(agent string) (Target, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	life, ok := h.lives[agent]
	st, connected := h.agents[agent]
	if !ok || life.forgotten() || !connected || st.conn == nil {
		return Target{}, false
	}
	return Target{Key: agent, Gen: life.gen}, true
}

// EnqueueTo queues cmd for generation t, as Enqueue does, and returns a
// Ticket that settles with it. cmd.ID, if set, is kept: re-queueing a
// command already queued under that id returns a ticket for the queued one,
// so a caller-derived id makes a repeat a no-op; a different command under
// a queued id is refused. It fails with ErrForgotten once t's generation is
// over.
func (h *Hub) EnqueueTo(t Target, cmd Command) (*Ticket, error) {
	p, err := h.enqueue(t.Key, &t.Gen, cmd)
	if err != nil {
		return nil, err
	}
	return &Ticket{ID: p.cmd.ID, p: p}, nil
}

// SendTo is Send for generation t: it fails with ErrForgotten, queueing
// nothing, once t's generation is over.
func (h *Hub) SendTo(ctx context.Context, t Target, cmd Command) error {
	p, err := h.enqueue(t.Key, &t.Gen, cmd)
	if err != nil {
		return err
	}
	return h.await(ctx, t.Key, p)
}

// ForgetGen forgets t's key, as Forget does, only while t is still its
// current generation, reporting whether it did. A launch that is over uses
// it, so a successor that has already reopened the key keeps its state.
func (h *Hub) ForgetGen(t Target) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.isCurrentLocked(t) {
		return false
	}
	h.forgetLocked(t.Key)
	return true
}

// ForgetGenUnlessConnected is ForgetGen that also keeps a connected stream,
// checked under the same lock (see ForgetUnlessConnected).
func (h *Hub) ForgetGenUnlessConnected(t Target) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.isCurrentLocked(t) {
		return false
	}
	if st, ok := h.agents[t.Key]; ok && st.conn != nil {
		return false
	}
	h.forgetLocked(t.Key)
	return true
}

func (h *Hub) isCurrentLocked(t Target) bool {
	life, ok := h.lives[t.Key]
	return ok && !life.forgotten() && life.gen == t.Gen
}

// HasConnected reports whether st is generation t's and its mod has
// connected or said hello. The stream need not be up right now: a mod
// between reconnects still has the launch. A generation's state only ever
// holds its own launch's (see Open), so nothing earlier can count.
func (st State) HasConnected(t Target) bool {
	if st.Agent != t.Key || st.Gen != t.Gen || st.Gen == 0 {
		return false
	}
	return !st.ConnectedAt.IsZero() || !st.HelloAt.IsZero()
}

// lifeLocked returns agent's live generation: ErrNotOpen for a key nobody
// opened, ErrForgotten while it is tombstoned.
func (h *Hub) lifeLocked(agent string) (*keyLife, error) {
	life, ok := h.lives[agent]
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: agent %s", ErrNotOpen, agent)
	case life.forgotten():
		return nil, fmt.Errorf("%w: agent %s", ErrForgotten, agent)
	}
	return life, nil
}

// launchLocked returns agent's live generation if launch is the one it is
// bound to: what a mod's stream or report must match. ErrForgotten while
// the key is tombstoned; ErrStaleLaunch for any other launch, and for a key
// nobody opened (a restarted daemon has not adopted the session yet).
func (h *Hub) launchLocked(agent, launch string) (*keyLife, error) {
	life, err := h.lifeLocked(agent)
	switch {
	case errors.Is(err, ErrNotOpen):
		return nil, fmt.Errorf("%w: agent %s has no open launch", ErrStaleLaunch, agent)
	case err != nil:
		return nil, err
	case life.launch != launch:
		return nil, fmt.Errorf("%w: agent %s launch %s", ErrStaleLaunch, agent, launch)
	}
	return life, nil
}

// tombstoneLocked marks agent forgotten until the next Open.
func (h *Hub) tombstoneLocked(agent string) {
	h.pruneTombstonesLocked()
	life, ok := h.lives[agent]
	if !ok {
		life = &keyLife{gen: h.nextGenLocked()}
		h.lives[agent] = life
	}
	life.forgottenAt = h.clock.Now()
}

func (h *Hub) pruneTombstonesLocked() {
	cutoff := h.clock.Now().Add(-TombstoneTTL)
	for agent, life := range h.lives {
		if life.forgotten() && life.forgottenAt.Before(cutoff) {
			delete(h.lives, agent)
		}
	}
}

// nextGenLocked returns a generation number never handed out before, so a
// key whose tombstone expired cannot repeat an old generation either.
func (h *Hub) nextGenLocked() uint64 {
	h.genSeq++
	return h.genSeq
}
