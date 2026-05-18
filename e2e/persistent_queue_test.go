//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPersistentQueueFIFOAndRejection drives three concurrent firings
// against a single persistent session with queue_max: 1. Expected:
//   - first firing is injected immediately,
//   - second firing waits in the queue,
//   - third firing is rejected with "queue full",
//   - after releasing the gate, the first two complete in FIFO order.
func TestPersistentQueueFIFOAndRejection(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-queue-*")

	// queue_max: 1 means the router accepts 1 in-flight + at most 1 queued.
	// A third concurrent firing should be rejected with "queue full".
	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
tasks:
  pulse:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/PULSE.md
    runtime: persistent
    queue_max: 1
    enabled: true
`, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts/PULSE.md"), []byte("Heartbeat.\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}
	g := newGatedResponder(cap)
	installGatedResponder(t, srv, dir, g)

	// Launch firings in background. The first two should block in await
	// until we release their gates; the third should exit fast with a
	// non-zero code because /task/enqueue rejected it.
	type result struct {
		idx    int
		stdout string
		stderr string
		code   int
		took   time.Duration
	}
	results := make(chan result, 3)
	var wg sync.WaitGroup

	fire := func(idx int) {
		defer wg.Done()
		start := time.Now()
		stdout, stderr, code := runLeo(t, dir, nil, "run", "pulse", "-c", cfgPath)
		results <- result{idx: idx, stdout: stdout, stderr: stderr, code: code, took: time.Since(start)}
	}

	wg.Add(1)
	go fire(1)
	// Give firing #1 time to actually enqueue + reach the injector before
	// the next firing arrives. Without this, #2 may race ahead of #1 and
	// the FIFO assertion below becomes meaningless.
	if err := waitFor(func() bool { return cap.len() >= 1 }, 2*time.Second); err != nil {
		t.Fatalf("first firing did not reach injector: %v", err)
	}

	wg.Add(1)
	go fire(2)
	// Wait until firing #2 is enqueued (still pending — depth = 2 now).
	// The router only injects firing #2 after firing #1's gate releases,
	// so cap.len() stays at 1 here. We give the router a beat to register
	// the enqueue.
	time.Sleep(150 * time.Millisecond)

	wg.Add(1)
	go fire(3)

	// Collect the third firing's result first: it should bail quickly
	// with a non-zero exit and "queue full" in stderr.
	var rejected result
	select {
	case r := <-results:
		rejected = r
	case <-time.After(3 * time.Second):
		t.Fatalf("third firing did not return within 3s (expected fast rejection)")
	}
	if rejected.code == 0 {
		t.Fatalf("expected non-zero exit for queue-full rejection, got 0; stderr=%q", rejected.stderr)
	}
	if !strings.Contains(rejected.stderr, "queue full") {
		t.Errorf("rejection stderr should mention 'queue full', got %q", rejected.stderr)
	}

	// Now release firings 1 then 2 in order. We need their invocation ids.
	// At this point the first injection has happened (cap[0]); release it
	// and wait for the second injection to follow (cap[1]).
	first := cap.snapshot()[0]
	g.release(first.InvID)

	if err := waitFor(func() bool { return cap.len() >= 2 }, 3*time.Second); err != nil {
		t.Fatalf("second firing did not reach injector after release: %v", err)
	}
	second := cap.snapshot()[1]
	if second.InvID == first.InvID {
		t.Fatalf("second injection has same invocation id as first: %s", first.InvID)
	}
	g.release(second.InvID)

	// Both runners should now exit successfully.
	var remaining []result
	timeout := time.After(5 * time.Second)
	for len(remaining) < 2 {
		select {
		case r := <-results:
			remaining = append(remaining, r)
		case <-timeout:
			t.Fatalf("only %d/2 firings completed after release", len(remaining))
		}
	}
	for _, r := range remaining {
		if r.code != 0 {
			t.Errorf("firing #%d exited %d; stderr=%q", r.idx, r.code, r.stderr)
		}
	}

	// Quick sanity check: rejected firing came back well before the
	// gates were released, proving the rejection was immediate rather
	// than a slow timeout.
	if rejected.took > 2*time.Second {
		t.Errorf("queue-full rejection took %s (should be ~immediate)", rejected.took)
	}

	wg.Wait()
}

// waitFor polls cond every 50ms up to timeout. Returns nil when cond is
// satisfied; otherwise returns a deadline-exceeded error.
func waitFor(cond func() bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}
