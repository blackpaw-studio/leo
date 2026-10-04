package bridge

import (
	"context"
	"fmt"
	"time"
)

// TombstoneTTL is how long a forgotten key refuses work before it is
// treated as never seen. It only has to outlive the last reports and
// reconnects of a launch that is already dead (the mod gives up retrying a
// report within about a minute).
const TombstoneTTL = time.Hour

// Target is one launch generation of an agent's bridge key. A launch that
// reuses a key (a relaunch of the same agent) opens a new generation, so a
// command routed to the old launch can be refused instead of reaching the
// new one.
type Target struct {
	Key string
	Gen uint64
}

// keyLife is a key's current generation and whether it is tombstoned.
type keyLife struct {
	gen uint64
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

// Open starts a new generation for agent's key ahead of a launch that
// connects under it, lifting the tombstone a Forget left. On a key that is
// live already (a surviving claude whose mod reconnected to a restarted
// daemon first) it keeps that state and returns the current generation.
func (h *Hub) Open(agent string) (Target, error) {
	if agent == "" {
		return Target{}, fmt.Errorf("%w: empty name", ErrInvalidAgent)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return Target{}, ErrClosed
	}
	h.pruneTombstonesLocked()
	life, ok := h.lives[agent]
	switch {
	case !ok:
		life = &keyLife{gen: h.nextGenLocked()}
		h.lives[agent] = life
	case life.forgotten():
		life.gen, life.forgottenAt = h.nextGenLocked(), time.Time{}
	}
	return Target{Key: agent, Gen: life.gen}, nil
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

// lifeLocked returns agent's live generation, starting one for a key the
// hub has never seen, or ErrForgotten while the key is tombstoned.
func (h *Hub) lifeLocked(agent string) (*keyLife, error) {
	life, ok := h.lives[agent]
	if !ok {
		life = &keyLife{gen: h.nextGenLocked()}
		h.lives[agent] = life
		return life, nil
	}
	if life.forgotten() {
		return nil, fmt.Errorf("%w: agent %s", ErrForgotten, agent)
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
