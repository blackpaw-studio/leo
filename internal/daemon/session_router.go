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
	mu          sync.Mutex
	fifo        []*PendingInvocation
	inFlight    *PendingInvocation // non-nil while one invocation is executing; nil otherwise
	notify      chan struct{}      // buffered(1); pump signal (used in Task 6)
	pumpStarted bool
}

type sessionRouter struct {
	mu     sync.Mutex
	queues map[string]*sessionQueue
	byID   map[string]*PendingInvocation
	inject injectFn
	abort  abortFn
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
// success, or ok=false if the session is at QueueMax (counting queued items
// plus any in-flight invocation). Does NOT block on the pump.
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
	// Capacity bounds active depth: queued items plus the one currently
	// executing (inFlight). Otherwise a fast pump drains the FIFO between
	// successive enqueues and queue_max never pushes back on the caller.
	depth := len(q.fifo)
	if q.inFlight != nil {
		depth++
	}
	if depth >= capacity {
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

type injectFn func(session, prompt string) error
type abortFn func(session string) error

// SetInjector / SetAborter wire the tmux primitives (or test fakes).
// Must be called before StartPump for any session.
func (r *sessionRouter) SetInjector(fn injectFn) {
	r.mu.Lock()
	r.inject = fn
	r.mu.Unlock()
}
func (r *sessionRouter) SetAborter(fn abortFn) {
	r.mu.Lock()
	r.abort = fn
	r.mu.Unlock()
}

func (r *sessionRouter) currentInjector() injectFn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inject
}

func (r *sessionRouter) currentAborter() abortFn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abort
}

// StartPump launches the per-session pump goroutine. Idempotent: a session
// only ever gets one pump in its lifetime (subsequent calls are no-ops).
func (r *sessionRouter) StartPump(session string) {
	r.mu.Lock()
	q, ok := r.queues[session]
	if !ok {
		q = &sessionQueue{notify: make(chan struct{}, 1)}
		r.queues[session] = q
	}
	if q.pumpStarted {
		r.mu.Unlock()
		return
	}
	q.pumpStarted = true
	r.mu.Unlock()
	go r.pump(session, q)
}

// Report signals the matching pending invocation. If id is unknown or doesn't
// match the session's current inFlight, the report is discarded silently
// (defensive against late hook callbacks).
func (r *sessionRouter) Report(id string, result InvocationResult) {
	inv, ok := r.Lookup(id)
	if !ok {
		return
	}
	q := r.queueFor(inv.Session)
	if q == nil {
		return
	}
	q.mu.Lock()
	matches := q.inFlight != nil && q.inFlight.ID == id
	if matches {
		q.inFlight = nil
	}
	q.mu.Unlock()
	if !matches {
		return // late / duplicate report
	}
	select {
	case inv.Result <- result:
	default:
	}
	r.mu.Lock()
	delete(r.byID, id)
	r.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (r *sessionRouter) pump(session string, q *sessionQueue) {
	for {
		<-q.notify
		for {
			q.mu.Lock()
			if q.inFlight != nil || len(q.fifo) == 0 {
				q.mu.Unlock()
				break
			}
			next := q.fifo[0]
			q.fifo = q.fifo[1:]
			q.inFlight = next
			q.mu.Unlock()

			if err := r.currentInjector()(session, next.Prompt); err != nil {
				q.mu.Lock()
				q.inFlight = nil
				q.mu.Unlock()
				r.mu.Lock()
				delete(r.byID, next.ID)
				r.mu.Unlock()
				select {
				case next.Result <- InvocationResult{OK: false, Err: "inject: " + err.Error()}:
				default:
				}
				continue
			}

			timer := time.NewTimer(next.Timeout)
			select {
			case <-timer.C:
				_ = r.currentAborter()(session)
				q.mu.Lock()
				still := q.inFlight != nil && q.inFlight.ID == next.ID
				if still {
					q.inFlight = nil
				}
				q.mu.Unlock()
				if still {
					r.mu.Lock()
					delete(r.byID, next.ID)
					r.mu.Unlock()
					select {
					case next.Result <- InvocationResult{OK: false, Err: "timeout"}:
					default:
					}
				}
			case <-q.notify:
				if !timer.Stop() {
					<-timer.C
				}
				// notify came from Report path — inFlight already cleared and
				// result delivered. Loop to pick up the next item.
			}
		}
	}
}
