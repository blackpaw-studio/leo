package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type EnqueueParams struct {
	Session     string // logical session name; the FIFO key
	TmuxSession string // concrete tmux session to inject/abort against
	Task        string
	Prompt      string   // already includes marker + delivery footer (caller's job)
	Channels    []string // for record-keeping only; delivery happens in-session
	QueueMax    int
	Timeout     time.Duration
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

	// completed is set under sessionRouter.mu once a terminal Result has been
	// posted to Result. The janitor reaps byID entries TTL after this stamp.
	completed time.Time
}

type sessionQueue struct {
	mu          sync.Mutex
	fifo        []*PendingInvocation
	inFlight    *PendingInvocation // non-nil while one invocation is executing; nil otherwise
	tmuxSession string             // concrete tmux session this queue injects into
	enqueueSig  chan struct{}      // buffered(1); fired by Enqueue
	reportSig   chan struct{}      // buffered(1); fired by Report / pump failure / ResetSession
	pumpStarted bool
}

func newSessionQueue() *sessionQueue {
	return &sessionQueue{
		enqueueSig: make(chan struct{}, 1),
		reportSig:  make(chan struct{}, 1),
	}
}

type sessionRouter struct {
	mu           sync.Mutex
	queues       map[string]*sessionQueue
	byID         map[string]*PendingInvocation
	inject       injectFn
	abort        abortFn
	done         chan struct{}
	stopOnce     sync.Once
	gcInterval   time.Duration
	completedTTL time.Duration
}

func newSessionRouter() *sessionRouter {
	r := &sessionRouter{
		queues:       map[string]*sessionQueue{},
		byID:         map[string]*PendingInvocation{},
		done:         make(chan struct{}),
		gcInterval:   30 * time.Second,
		completedTTL: 5 * time.Minute,
	}
	go r.janitor()
	return r
}

func newInvocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Stop signals all running pump and janitor goroutines to exit. Idempotent.
func (r *sessionRouter) Stop() {
	r.stopOnce.Do(func() { close(r.done) })
}

// defaultTimeout is the fallback for non-positive enqueue timeouts. The HTTP
// handler enforces the same bound; this is belt-and-suspenders for in-process
// callers of EnqueueWithID.
const defaultEnqueueTimeout = 5 * time.Minute

// Enqueue appends to the session's FIFO. Returns the invocation and ok=true on
// success, or ok=false if the session is at QueueMax (counting queued items
// plus any in-flight invocation). Does NOT block on the pump.
func (r *sessionRouter) Enqueue(p EnqueueParams) (*PendingInvocation, bool) {
	return r.EnqueueWithID("", p)
}

// EnqueueWithID is like Enqueue but uses the supplied id (when non-empty)
// instead of generating one. Returns ok=false if the queue is at QueueMax.
// This is what lets a runner pre-bake an invocation id into its prompt marker
// and have the daemon track the same id server-side.
func (r *sessionRouter) EnqueueWithID(id string, p EnqueueParams) (*PendingInvocation, bool) {
	if p.Timeout <= 0 {
		p.Timeout = defaultEnqueueTimeout
	}
	r.mu.Lock()
	q, ok := r.queues[p.Session]
	if !ok {
		q = newSessionQueue()
		r.queues[p.Session] = q
	}
	r.mu.Unlock()

	q.mu.Lock()
	defer q.mu.Unlock()
	// Record the tmux target for this session (all invocations share it).
	// Falls back to the logical key if the caller didn't resolve one.
	if p.TmuxSession != "" {
		q.tmuxSession = p.TmuxSession
	} else if q.tmuxSession == "" {
		q.tmuxSession = p.Session
	}
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
	if id == "" {
		id = newInvocationID()
	}
	inv := &PendingInvocation{
		ID:       id,
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
	case q.enqueueSig <- struct{}{}:
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

// QueueDepth returns the number of queued plus in-flight invocations for a
// session, or 0 if no queue has ever been created for it.
func (r *sessionRouter) QueueDepth(session string) int {
	q := r.queueFor(session)
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	depth := len(q.fifo)
	if q.inFlight != nil {
		depth++
	}
	return depth
}

type injectFn func(tmuxSession, prompt string) error
type abortFn func(tmuxSession string) error

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
		q = newSessionQueue()
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
	r.markCompleted(inv, result)
	// Wake the pump so it can pick up the next item or exit the inner select.
	select {
	case q.reportSig <- struct{}{}:
	default:
	}
}

// ResetSession drains all in-flight and queued invocations for the given
// session, delivering an error result to each. Intended for `leo session
// reset` to recover when the underlying claude is killed/restarted outside
// the pump's awareness. Does NOT call the aborter (tmux is presumed already
// killed). After this call the queue is empty and the pump is ready to
// accept new work.
func (r *sessionRouter) ResetSession(session, reason string) int {
	q := r.queueFor(session)
	if q == nil {
		return 0
	}
	q.mu.Lock()
	inv := q.inFlight
	pending := q.fifo
	q.inFlight = nil
	q.fifo = nil
	q.mu.Unlock()

	cleared := 0
	if inv != nil {
		r.markCompleted(inv, InvocationResult{OK: false, Err: "reset: " + reason})
		cleared++
	}
	for _, p := range pending {
		r.markCompleted(p, InvocationResult{OK: false, Err: "reset: " + reason})
		cleared++
	}
	// Wake the pump so any inner-select wait observes the cleared inFlight.
	select {
	case q.reportSig <- struct{}{}:
	default:
	}
	return cleared
}

// markCompleted stamps completion time and delivers a buffered result. The
// byID entry is left in place so a slow AwaitTask caller can still observe
// the result; the janitor reaps it after completedTTL.
func (r *sessionRouter) markCompleted(inv *PendingInvocation, result InvocationResult) {
	r.mu.Lock()
	inv.completed = time.Now()
	r.mu.Unlock()
	select {
	case inv.Result <- result:
	default:
	}
}

// janitor periodically reaps byID entries whose completion stamp is older
// than completedTTL. Stops when r.done is closed.
func (r *sessionRouter) janitor() {
	t := time.NewTicker(r.gcInterval)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		now := time.Now()
		r.mu.Lock()
		for id, inv := range r.byID {
			if !inv.completed.IsZero() && now.Sub(inv.completed) > r.completedTTL {
				delete(r.byID, id)
			}
		}
		r.mu.Unlock()
	}
}

func (r *sessionRouter) pump(session string, q *sessionQueue) {
	for {
		select {
		case <-r.done:
			return
		case <-q.enqueueSig:
		case <-q.reportSig:
		}
		for {
			q.mu.Lock()
			if q.inFlight != nil || len(q.fifo) == 0 {
				q.mu.Unlock()
				break
			}
			next := q.fifo[0]
			q.fifo = q.fifo[1:]
			q.inFlight = next
			target := q.tmuxSession
			q.mu.Unlock()

			if err := r.currentInjector()(target, next.Prompt); err != nil {
				q.mu.Lock()
				if q.inFlight != nil && q.inFlight.ID == next.ID {
					q.inFlight = nil
				}
				q.mu.Unlock()
				r.markCompleted(next, InvocationResult{OK: false, Err: "inject: " + err.Error()})
				continue
			}

			// A ResetSession (or Report) may have cleared inFlight while we
			// were inside the injector. If so, the result was already
			// delivered; abort the turn we just started so the orphaned
			// prompt doesn't keep running, then move on.
			q.mu.Lock()
			stillMine := q.inFlight != nil && q.inFlight.ID == next.ID
			q.mu.Unlock()
			if !stillMine {
				_ = r.currentAborter()(target)
				continue
			}

			timer := time.NewTimer(next.Timeout)
			select {
			case <-r.done:
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
				// Re-check inFlight BEFORE aborting: a Report that landed just
				// as the timer fired already delivered the result and moved the
				// session on to its next turn, so aborting here would Ctrl-C a
				// turn that legitimately completed.
				q.mu.Lock()
				still := q.inFlight != nil && q.inFlight.ID == next.ID
				if still {
					q.inFlight = nil
				}
				q.mu.Unlock()
				if still {
					_ = r.currentAborter()(target)
					r.markCompleted(next, InvocationResult{OK: false, Err: "timeout"})
				}
			case <-q.reportSig:
				if !timer.Stop() {
					<-timer.C
				}
				// Report / Reset path: inFlight already cleared and result
				// delivered. Loop to pick up the next item.
			}
		}
	}
}
