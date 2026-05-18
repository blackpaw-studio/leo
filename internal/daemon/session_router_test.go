package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionRouterEnqueueAccepts(t *testing.T) {
	r := newSessionRouter()
	inv, ok := r.Enqueue(EnqueueParams{
		Session:  "leo-session-foo",
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
	injector := func(session, prompt string) error {
		injMu.Lock()
		injections = append(injections, session+"|"+prompt)
		injMu.Unlock()
		return nil
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
	r.SetInjector(func(string, string) error { return nil })
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
	r.SetInjector(func(string, string) error { return nil })
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

// TestSessionRouterResetClearsInFlight verifies that ResetSession delivers a
// "reset" error to the in-flight waiter and any queued items, and that the
// pump resumes accepting new work afterwards.
func TestSessionRouterResetClearsInFlight(t *testing.T) {
	r := newSessionRouter()
	defer r.Stop()
	var injected int32
	r.SetInjector(func(string, string) error {
		atomic.AddInt32(&injected, 1)
		return nil
	})
	r.SetAborter(func(string) error { return nil })

	inv1, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "stuck", Prompt: "p1", QueueMax: 5, Timeout: 10 * time.Second})
	inv2, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "queued", Prompt: "p2", QueueMax: 5, Timeout: 10 * time.Second})
	r.StartPump("s")
	time.Sleep(20 * time.Millisecond)
	if cleared := r.ResetSession("s", "test"); cleared != 2 {
		t.Fatalf("ResetSession cleared %d, want 2", cleared)
	}
	for _, inv := range []*PendingInvocation{inv1, inv2} {
		select {
		case res := <-inv.Result:
			if res.OK || res.Err == "" {
				t.Fatalf("expected reset error result, got %+v", res)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("reset did not deliver result for %s", inv.Task)
		}
	}
	// New enqueue should run normally after reset.
	inv3, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "after", Prompt: "p3", QueueMax: 5, Timeout: time.Second})
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Report(inv3.ID, InvocationResult{OK: true, FinalMessage: "ok"})
	}()
	select {
	case res := <-inv3.Result:
		if !res.OK {
			t.Fatalf("post-reset enqueue failed: %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("post-reset enqueue never ran")
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
	r.SetInjector(func(session, prompt string) error { return nil })
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
