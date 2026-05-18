package daemon

import (
	"sync"
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
