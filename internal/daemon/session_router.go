package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type EnqueueParams struct {
	Session  string
	Task     string
	Prompt   string   // already includes marker + delivery footer (caller's job)
	Channels []string // for record-keeping only; delivery happens in-session
	QueueMax int
	Timeout  time.Duration
}

type InvocationResult struct {
	OK           bool
	SessionID    string
	FinalMessage string
	Err          string
}

type PendingInvocation struct {
	ID       string
	Session  string
	Task     string
	Prompt   string
	Channels []string
	Timeout  time.Duration
	Enqueued time.Time
	Result   chan InvocationResult // buffered(1); never close from inside the queue
}

type sessionQueue struct {
	mu       sync.Mutex
	fifo     []*PendingInvocation
	inFlight *PendingInvocation
	notify   chan struct{} // buffered(1); pump signal (used in Task 6)
}

type sessionRouter struct {
	mu     sync.Mutex
	queues map[string]*sessionQueue
	byID   map[string]*PendingInvocation
}

func newSessionRouter() *sessionRouter {
	return &sessionRouter{
		queues: map[string]*sessionQueue{},
		byID:   map[string]*PendingInvocation{},
	}
}

func newInvocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Enqueue appends to the session's FIFO. Returns the invocation and ok=true on
// success, or ok=false if the queue is at QueueMax. Does NOT block on the pump
// (which is not yet implemented in this task).
func (r *sessionRouter) Enqueue(p EnqueueParams) (*PendingInvocation, bool) {
	r.mu.Lock()
	q, ok := r.queues[p.Session]
	if !ok {
		q = &sessionQueue{notify: make(chan struct{}, 1)}
		r.queues[p.Session] = q
	}
	r.mu.Unlock()

	q.mu.Lock()
	defer q.mu.Unlock()
	capacity := p.QueueMax
	if capacity <= 0 {
		capacity = 5
	}
	if len(q.fifo) >= capacity {
		return nil, false
	}
	inv := &PendingInvocation{
		ID:       newInvocationID(),
		Session:  p.Session,
		Task:     p.Task,
		Prompt:   p.Prompt,
		Channels: p.Channels,
		Timeout:  p.Timeout,
		Enqueued: time.Now(),
		Result:   make(chan InvocationResult, 1),
	}
	q.fifo = append(q.fifo, inv)

	r.mu.Lock()
	r.byID[inv.ID] = inv
	r.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
	return inv, true
}

// Lookup returns the invocation by id, or false if missing/expired.
func (r *sessionRouter) Lookup(id string) (*PendingInvocation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.byID[id]
	return inv, ok
}

// queueFor returns the named session queue (or nil if none ever enqueued).
func (r *sessionRouter) queueFor(session string) *sessionQueue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queues[session]
}
