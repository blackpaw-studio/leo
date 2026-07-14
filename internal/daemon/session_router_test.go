package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestSessionRouterEnqueueAccepts(t *testing.T) {
	r := newSessionRouter()
	inv, ok := r.Enqueue(EnqueueParams{
		Session:  "leo-agent-foo",
		Task:     "morning",
		Prompt:   "do the thing",
		Channels: []string{"plugin:slack@official"},
		QueueMax: 5,
		Timeout:  10 * time.Second,
	})
	if !ok {
		t.Fatalf("expected enqueue accepted")
	}
	if inv.ID == "" {
		t.Fatalf("expected non-empty invocation id")
	}
	if inv.Task != "morning" {
		t.Fatalf("task mismatch: %q", inv.Task)
	}
}

func TestSessionRouterEnqueueQueueFull(t *testing.T) {
	r := newSessionRouter()
	p := EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 2, Timeout: time.Second}
	if _, ok := r.Enqueue(p); !ok {
		t.Fatal("first enqueue should accept")
	}
	if _, ok := r.Enqueue(p); !ok {
		t.Fatal("second enqueue should accept (queue depth 2)")
	}
	if _, ok := r.Enqueue(p); ok {
		t.Fatal("third enqueue should reject (queue full)")
	}
}

func TestSessionRouterLookupByID(t *testing.T) {
	r := newSessionRouter()
	inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 5, Timeout: time.Second})
	got, ok := r.Lookup(inv.ID)
	if !ok || got.Task != "t" {
		t.Fatalf("lookup failed: %+v ok=%v", got, ok)
	}
	if _, ok := r.Lookup("does-not-exist"); ok {
		t.Fatalf("expected miss for unknown id")
	}
}

func TestSessionRouterPumpInjectsThenAdvances(t *testing.T) {
	r := newSessionRouter()
	var injMu sync.Mutex
	var injections []string
	injector := func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		injMu.Lock()
		injections = append(injections, session+"|"+prompt)
		injMu.Unlock()
		return nil, nil
	}
	r.SetInjector(injector)
	r.SetAborter(func(string) error { return nil })

	inv1, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "a", Prompt: "p1", QueueMax: 5, Timeout: time.Second})
	inv2, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "b", Prompt: "p2", QueueMax: 5, Timeout: time.Second})
	r.StartPump("s")

	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Report(inv1.ID, InvocationResult{OK: true, FinalMessage: "done1"})
		time.Sleep(20 * time.Millisecond)
		r.Report(inv2.ID, InvocationResult{OK: true, FinalMessage: "done2"})
	}()

	res1 := <-inv1.Result
	res2 := <-inv2.Result
	if !res1.OK || res1.FinalMessage != "done1" {
		t.Fatalf("res1 wrong: %+v", res1)
	}
	if !res2.OK || res2.FinalMessage != "done2" {
		t.Fatalf("res2 wrong: %+v", res2)
	}
	injMu.Lock()
	defer injMu.Unlock()
	if len(injections) != 2 || injections[0] != "s|p1" || injections[1] != "s|p2" {
		t.Fatalf("injection order wrong: %v", injections)
	}
}

// TestSessionRouterTimeoutFiresDespiteConcurrentEnqueue guards against the
// regression where Enqueue and Report shared a single notify channel: an
// Enqueue during an in-flight invocation would wake the pump's inner select,
// stop the timer, and leave the in-flight task without a working timeout.
// Now the two are separate signals; the timer must still fire even when a
// second enqueue arrives mid-flight and Report never comes.
func TestSessionRouterTimeoutFiresDespiteConcurrentEnqueue(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	var aborted int32
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) { return nil, nil })
	r.SetAborter(func(string) error {
		atomic.AddInt32(&aborted, 1)
		return nil
	})

	inv1, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "slow", Prompt: "p1", QueueMax: 5, Timeout: 80 * time.Millisecond})
	r.StartPump("s")
	// Race a second enqueue in while inv1 is in-flight.
	time.Sleep(20 * time.Millisecond)
	inv2, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "next", Prompt: "p2", QueueMax: 5, Timeout: 200 * time.Millisecond})

	select {
	case res := <-inv1.Result:
		if res.OK || res.Err != "timeout" {
			t.Fatalf("inv1 expected timeout, got %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("inv1 timeout never fired — pump timer was disarmed by concurrent enqueue")
	}
	if atomic.LoadInt32(&aborted) == 0 {
		t.Fatalf("aborter was not called after timeout")
	}
	// inv2 should now run; report it to confirm pump advanced.
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Report(inv2.ID, InvocationResult{OK: true, FinalMessage: "done"})
	}()
	select {
	case res := <-inv2.Result:
		if !res.OK {
			t.Fatalf("inv2 expected ok, got %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("inv2 never ran after inv1 timeout")
	}
}

// TestSessionRouterAwaitAfterReportSucceeds guards against the race where
// Report deleted byID before the AwaitTask handler reached Lookup. The fix
// keeps byID entries alive (reaped lazily by the janitor) so a slow Lookup
// still finds the invocation and reads the buffered result.
func TestSessionRouterAwaitAfterReportSucceeds(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) { return nil, nil })
	r.SetAborter(func(string) error { return nil })

	inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "p", QueueMax: 5, Timeout: time.Second})
	r.StartPump("s")
	// Wait for pump to pick it up, then report immediately.
	time.Sleep(20 * time.Millisecond)
	r.Report(inv.ID, InvocationResult{OK: true, FinalMessage: "ok"})
	// A later Lookup must still find the invocation.
	got, ok := r.Lookup(inv.ID)
	if !ok {
		t.Fatalf("Lookup returned false after Report — byID was deleted prematurely")
	}
	select {
	case res := <-got.Result:
		if !res.OK {
			t.Fatalf("result not delivered: %+v", res)
		}
	default:
		t.Fatalf("Result channel had no buffered value after Report")
	}
}

// TestSessionRouterEnqueueTimeoutClamp verifies that an in-process caller
// passing Timeout=0 does not get an immediate abort (the timer would fire
// on a zero duration). The router clamps to the default.
func TestSessionRouterEnqueueTimeoutClamp(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	inv, ok := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "p", Timeout: 0})
	if !ok {
		t.Fatalf("enqueue rejected")
	}
	if inv.Timeout != defaultEnqueueTimeout {
		t.Fatalf("timeout not clamped: %v", inv.Timeout)
	}
}

func TestSessionRouterPumpTimeoutAborts(t *testing.T) {
	r := newSessionRouter()
	var abMu sync.Mutex
	var aborted bool
	r.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) { return nil, nil })
	r.SetAborter(func(session string) error {
		abMu.Lock()
		aborted = true
		abMu.Unlock()
		return nil
	})

	inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "slow", Prompt: "x", QueueMax: 5, Timeout: 50 * time.Millisecond})
	r.StartPump("s")
	res := <-inv.Result
	if res.OK || res.Err != "timeout" {
		t.Fatalf("expected timeout, got %+v", res)
	}
	abMu.Lock()
	defer abMu.Unlock()
	if !aborted {
		t.Fatalf("expected aborter to be called")
	}
}

// TestSessionRouterInjectsTmuxTarget verifies the pump injects into (and
// aborts) the concrete tmux session target carried on the invocation, NOT the
// bare logical session key used for FIFO routing. A persistent session keyed
// "daily" actually lives in tmux as "leo-agent-daily"; injecting into the
// bare key targets a nonexistent session.
func TestSessionRouterInjectsTmuxTarget(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	var mu sync.Mutex
	var injectedInto string
	r.SetInjector(func(ctx context.Context, target, _ string) (*harness.Result, error) {
		mu.Lock()
		injectedInto = target
		mu.Unlock()
		return nil, nil
	})
	r.SetAborter(func(string) error { return nil })

	inv, ok := r.Enqueue(EnqueueParams{
		Session:     "daily",           // FIFO key (bare logical name)
		TmuxSession: "leo-agent-daily", // concrete tmux target
		Task:        "t",
		Prompt:      "p",
		QueueMax:    5,
		Timeout:     time.Second,
	})
	if !ok {
		t.Fatal("enqueue rejected")
	}
	r.StartPump("daily")
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Report(inv.ID, InvocationResult{OK: true})
	}()
	<-inv.Result

	mu.Lock()
	defer mu.Unlock()
	if injectedInto != "leo-agent-daily" {
		t.Fatalf("injector targeted %q, want tmux session %q", injectedInto, "leo-agent-daily")
	}
}

// TestSessionRouterAbortsTmuxTargetOnTimeout verifies the timeout path aborts
// the concrete tmux target, not the bare key.
func TestSessionRouterAbortsTmuxTargetOnTimeout(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	var mu sync.Mutex
	var abortedTarget string
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) { return nil, nil })
	r.SetAborter(func(target string) error {
		mu.Lock()
		abortedTarget = target
		mu.Unlock()
		return nil
	})
	inv, _ := r.Enqueue(EnqueueParams{
		Session:     "web",
		TmuxSession: "leo-web",
		Task:        "t",
		Prompt:      "p",
		QueueMax:    5,
		Timeout:     30 * time.Millisecond,
	})
	r.StartPump("web")
	<-inv.Result // resolves via timeout
	mu.Lock()
	defer mu.Unlock()
	if abortedTarget != "leo-web" {
		t.Fatalf("aborter targeted %q, want %q", abortedTarget, "leo-web")
	}
}

// TestSessionRouterReportDuringInjectAbortsZombie reproduces the race where a
// Report call clears inFlight while the pump is still inside the injector.
// Without the post-inject re-check, the pump would start a timer on an
// already-cleared invocation and the freshly-injected (now orphaned) turn
// would keep running in tmux. The pump must instead abort the zombie turn
// and move on; the result must be delivered exactly once.
func TestSessionRouterReportDuringInjectAbortsZombie(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	injectStarted := make(chan struct{})
	releaseInject := make(chan struct{})
	var aborted int32
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) {
		close(injectStarted)
		<-releaseInject
		return nil, nil
	})
	r.SetAborter(func(string) error {
		atomic.AddInt32(&aborted, 1)
		return nil
	})
	inv, _ := r.Enqueue(EnqueueParams{
		Session:     "s",
		TmuxSession: "leo-agent-s",
		Task:        "t",
		Prompt:      "p",
		QueueMax:    5,
		Timeout:     time.Hour, // long; the test never wants a timeout
	})
	r.StartPump("s")

	<-injectStarted // pump is inside the injector, inFlight=inv
	r.Report(inv.ID, InvocationResult{OK: false, Err: "killed"})
	close(releaseInject) // injector returns; pump re-checks inFlight

	res := <-inv.Result
	if res.OK {
		t.Fatalf("expected error result, got OK")
	}
	// The pump must abort the orphaned turn it just injected.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&aborted) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&aborted) != 1 {
		t.Fatalf("expected zombie turn aborted exactly once, got %d", atomic.LoadInt32(&aborted))
	}
}

// TestSessionRouterSyncInjectorCompletesImmediately verifies that when the
// injector returns a non-nil *harness.Result (a synchronous-completion
// signal; no production driver returns one today — every real driver is a
// fire-and-forget tmux-TUI paste — this exercises the pump's branch for a
// hypothetical future synchronous injector), the pump marks the invocation
// completed right away — no Report call needed — surfacing the result text
// and session id exactly like the Report path would.
func TestSessionRouterSyncInjectorCompletesImmediately(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	var reportCalls int32
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) {
		return &harness.Result{Text: "done", SessionID: "t1"}, nil
	})
	r.SetAborter(func(string) error { return nil })

	inv, ok := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "p", QueueMax: 5, Timeout: time.Second})
	if !ok {
		t.Fatal("enqueue rejected")
	}
	r.StartPump("s")

	select {
	case res := <-inv.Result:
		if !res.OK {
			t.Fatalf("expected OK completion, got %+v", res)
		}
		if res.FinalMessage != "done" {
			t.Fatalf("FinalMessage = %q, want %q", res.FinalMessage, "done")
		}
		if res.SessionID != "t1" {
			t.Fatalf("SessionID = %q, want %q", res.SessionID, "t1")
		}
	case <-time.After(time.Second):
		t.Fatalf("sync-completed invocation never delivered a result")
	}
	if atomic.LoadInt32(&reportCalls) != 0 {
		t.Fatalf("Report should never have been called for a synchronous completion")
	}
}

// TestSessionRouterSyncInjectorErrorMarksFailed mirrors the happy-path test
// above for the IsError case: the invocation must be marked failed, not OK.
func TestSessionRouterSyncInjectorErrorMarksFailed(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) {
		return &harness.Result{Text: "boom", IsError: true, SessionID: "t2"}, nil
	})
	r.SetAborter(func(string) error { return nil })

	inv, ok := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "p", QueueMax: 5, Timeout: time.Second})
	if !ok {
		t.Fatal("enqueue rejected")
	}
	r.StartPump("s")

	select {
	case res := <-inv.Result:
		if res.OK {
			t.Fatalf("expected failed completion, got OK: %+v", res)
		}
		if res.Err == "" {
			t.Fatalf("expected a non-empty error message")
		}
		if res.SessionID != "t2" {
			t.Fatalf("SessionID = %q, want %q", res.SessionID, "t2")
		}
	case <-time.After(time.Second):
		t.Fatalf("sync-completed error invocation never delivered a result")
	}
}

// TestSessionRouterInjectorTimeoutKillsHungTurn verifies that the pump
// threads the invocation's Timeout into the ctx it passes to the injector
// (see the injCtx wrap in the pump loop): an injector whose call respects ctx
// cancellation must fail fast when the pump's timeout elapses, rather than
// the pump only discovering the hang via its own outer per-invocation timer.
// This is what lets a real tmux-paste injector's exec.CommandContext actually
// kill a hung invocation's subprocess (probe/paste/dismissal helpers all shell
// out).
func TestSessionRouterInjectorTimeoutKillsHungTurn(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	r.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		select {
		case <-time.After(2 * time.Second):
			return &harness.Result{Text: "too slow"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	r.SetAborter(func(string) error { return nil })

	inv, ok := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "p", QueueMax: 5, Timeout: 30 * time.Millisecond})
	if !ok {
		t.Fatal("enqueue rejected")
	}
	r.StartPump("s")

	select {
	case res := <-inv.Result:
		if res.OK {
			t.Fatalf("expected failed completion, got OK: %+v", res)
		}
		if res.Err == "" {
			t.Fatalf("expected a non-empty error message")
		}
	case <-time.After(time.Second):
		t.Fatalf("hung-turn invocation never completed after the injector's ctx deadline elapsed")
	}
}
