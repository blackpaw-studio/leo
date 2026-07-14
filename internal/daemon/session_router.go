package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type EnqueueParams struct {
	Session     string // logical session name; the FIFO key
	TmuxSession string // concrete tmux session to inject/abort against
	Task        string
	Prompt      string   // already includes marker + delivery footer (caller's job)
	Channels    []string // for record-keeping only; delivery happens in-session
	QueueMax    int
	Timeout     time.Duration
	// Ensure, when non-nil, tells the pump to make sure the target agent is
	// injectable (spawn/resume as needed) before injecting this invocation's
	// prompt. Nil skips the ensure step (used by callers that inject directly
	// without an agent target, e.g. tests).
	Ensure *EnsureSpec
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
	// Ensure carries the ensure-exists spec from EnqueueParams (nil when the
	// caller injects directly without an agent target). The pump runs it just
	// before injection.
	Ensure *EnsureSpec

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
	reportSig   chan struct{}      // buffered(1); fired by Report / pump failure
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
	ensurer      AgentEnsurer
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
		Ensure:   p.Ensure,
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

// injectFn delivers a prompt into a session. A nil *harness.Result means
// delivery is asynchronous — every real driver today (claude, codex,
// opencode) pastes into its tmux-TUI pane and returns immediately; completion
// arrives later via Report or the pump's timeout. A non-nil *harness.Result
// means the turn already ran to completion synchronously; the pump marks the
// invocation completed immediately instead of waiting. No production driver
// returns a non-nil Result today — the branch exists as the insertion point
// for a future synchronous injector.
//
// ctx carries the invocation's deadline (set by the pump from
// PendingInvocation.Timeout — see the pump loop below): a hypothetical
// synchronous injector would need to thread it into its own
// exec.CommandContext so a hung call is actually killed, not just abandoned.
// Every real driver's tmux paste call completes almost instantly, so the
// deadline is inert there — the pump's own outer timer (unchanged) still
// governs the async wait for a Report.
type injectFn func(ctx context.Context, tmuxSession, prompt string) (*harness.Result, error)
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

// SetEnsurer wires the ensure-exists step the pump runs before injecting an
// invocation that carries an EnsureSpec. Optional: nil (never called, or
// explicitly set to nil) is safe — invocations without an EnsureSpec never
// consult it.
func (r *sessionRouter) SetEnsurer(e AgentEnsurer) {
	r.mu.Lock()
	r.ensurer = e
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

func (r *sessionRouter) currentEnsurer() AgentEnsurer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensurer
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

// invocationResultFromHarness translates a synchronous driver Result into the
// same InvocationResult shape the Report path builds (see handleTaskReport),
// so both completion sources are indistinguishable to AwaitTask callers.
func invocationResultFromHarness(res *harness.Result) InvocationResult {
	if res.IsError {
		errMsg := res.Text
		if errMsg == "" && len(res.Errors) > 0 {
			errMsg = res.Errors[0]
		}
		return InvocationResult{OK: false, SessionID: res.SessionID, Err: errMsg}
	}
	return InvocationResult{OK: true, SessionID: res.SessionID, FinalMessage: res.Text}
}

// completeInFlight clears q.inFlight (if it still matches inv) and delivers
// result via markCompleted. Shared by the pump's synchronous-injector path
// and mirrors the bookkeeping Report performs for the async path.
func (r *sessionRouter) completeInFlight(q *sessionQueue, inv *PendingInvocation, result InvocationResult) {
	q.mu.Lock()
	if q.inFlight != nil && q.inFlight.ID == inv.ID {
		q.inFlight = nil
	}
	q.mu.Unlock()
	r.markCompleted(inv, result)
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

			// Ensure-exists step: invocations carrying an EnsureSpec (the
			// agent-routed persistent-task path) need their target agent
			// spawned or resumed before injection can succeed. A nil Ensure
			// skips this entirely. A failed ensure completes the invocation
			// as failed through the same path an inject error takes, so
			// AwaitTask callers (and notify_on_fail) observe it exactly like
			// any other delivery failure. This runs on the per-session pump
			// goroutine, so a slow spawn only blocks this one queue — other
			// sessions/agents keep draining on their own goroutines.
			if next.Ensure != nil {
				if ensurer := r.currentEnsurer(); ensurer != nil {
					ensureCtx, ensureCancel := context.WithTimeout(context.Background(), next.Timeout)
					err := ensurer.Ensure(ensureCtx, *next.Ensure)
					ensureCancel()
					if err != nil {
						r.completeInFlight(q, next, InvocationResult{OK: false, Err: "ensure: " + err.Error()})
						continue
					}
				}
			}

			// injCtx bounds the injector call itself to the invocation's
			// timeout: every real driver's Inject is a readiness-probed tmux
			// paste that returns almost instantly, so in practice this
			// deadline just backstops a wedged pane or hung probe loop (via
			// exec.CommandContext deep inside the driver) rather than a
			// synchronous turn — no production driver blocks for a whole
			// turn today. Completion for all of them still relies on the
			// outer timer below for the async Report wait (see the
			// KNOWN LIMITATION note further down).
			injCtx, injCancel := context.WithTimeout(context.Background(), next.Timeout)
			res, err := r.currentInjector()(injCtx, target, next.Prompt)
			injCancel()
			if err != nil {
				r.completeInFlight(q, next, InvocationResult{OK: false, Err: "inject: " + err.Error()})
				continue
			}

			// A non-nil Result means the injector's driver ran the turn to
			// completion synchronously — mark the invocation completed
			// immediately using the same bookkeeping the Report path uses,
			// and skip the await-Report/timeout window entirely. Nil Result
			// keeps today's async wait byte-identical.
			if res != nil {
				r.completeInFlight(q, next, invocationResultFromHarness(res))
				continue
			}

			// KNOWN LIMITATION: every driver's Inject is now fire-and-forget
			// (nil Result on success, claude parity) — including dispatched
			// codex/opencode SESSIONS routed through this pump. Those
			// harnesses drive a resident TUI with no Stop-hook-style Report
			// callback, so a queued invocation here falls through to the
			// timeout branch below and completes via the timer (abort +
			// "timeout" result) rather than a genuine turn-done signal. This
			// does not affect ephemeral agents (they use the direct
			// fire-and-forget path in internal/web/handlers.go, not this
			// router) or claude sessions (which still get a Stop-hook
			// Report). Tracked as a follow-up: a TUI turn-completion signal
			// for non-claude sessions. Do not "fix" this by synthesizing a
			// delivered Result here — that's a deliberate, deferred design
			// decision.

			// A Report may have cleared inFlight while we were inside the
			// injector. If so, the result was already delivered; abort the
			// turn we just started so the orphaned prompt doesn't keep
			// running, then move on.
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
