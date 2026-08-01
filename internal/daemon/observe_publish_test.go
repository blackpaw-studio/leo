package daemon

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// TestPublishTaskRunReachesDaemonRunLog is the regression test for the
// blocker: a subprocess (leo run <task>) has no in-process access to the
// daemon's RunLog, so it must report task-run events over the existing
// Unix-socket IPC. This drives the real client wrapper (PublishTaskRun)
// against a real daemon Server with SetObservability wired exactly as
// RunSupervised wires it, and asserts the event lands in the RunLog the
// daemon holds — the same object /api/v1/state and /api/v1/events read.
func TestPublishTaskRunReachesDaemonRunLog(t *testing.T) {
	workDir := tmpWorkDir(t)
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil)
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s.SetObservability(bus, runLog, nil, "v-test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := PublishTaskRun(ctx, workDir, observe.EventTaskRunStarted, observe.TaskRun{
		ID:        "publish-test-1",
		Task:      "publish-test",
		Status:    observe.RunRunning,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("PublishTaskRun() error: %v", err)
	}

	recent := runLog.Recent(0)
	found := false
	for _, r := range recent {
		if r.ID == "publish-test-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected run log to contain publish-test-1, got %+v", recent)
	}
}

// TestPublishTaskRunNoDaemonIsSilentAndFast is the non-fatal contract: a
// manual `leo run` on a box with no daemon running must still execute the
// task normally, so PublishTaskRun against an unreachable socket must return
// quickly (well under the caller's own timeout) rather than hanging.
func TestPublishTaskRunNoDaemonIsSilentAndFast(t *testing.T) {
	workDir := tmpWorkDir(t) // state/ exists, but nothing is listening

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := PublishTaskRun(ctx, workDir, observe.EventTaskRunStarted, observe.TaskRun{
		ID:   "no-daemon-1",
		Task: "no-daemon",
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when no daemon is listening")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("PublishTaskRun took %v against an unreachable socket — must fail fast", elapsed)
	}
}

// TestPublishTaskRunTimesOutWhenDaemonNeverResponds is the regression test
// for finding #4: the ENOENT case above dials in microseconds and passes
// identically whether observeTaskRunPublishTimeout is 2s or 10 minutes, so it
// can't catch a regression in the timeout itself. This drives the scenario
// that actually matters — a daemon that accepts the connection and then
// never responds — with a real Unix listener, and asserts PublishTaskRun
// returns in roughly observeTaskRunPublishTimeout rather than blocking for
// the ctx deadline (which production callers set far longer, since a task
// timeout runs in minutes).
func TestPublishTaskRunTimesOutWhenDaemonNeverResponds(t *testing.T) {
	workDir := tmpWorkDir(t)
	sockPath := SockPath(workDir)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on %s: %v", sockPath, err)
	}
	// parked collects every accepted connection so cleanup can close them —
	// closing inside the accept loop would answer the client, defeating the
	// "never responds" scenario this test exists to cover.
	var mu sync.Mutex
	var parked []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range parked {
			_ = c.Close()
		}
	})

	// acceptAndPark takes every incoming connection and holds it open,
	// reading and writing nothing — the client's request is accepted at the
	// transport level but never answered. The loop ends when ln.Close (in
	// cleanup) makes Accept return an error.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			parked = append(parked, conn)
			mu.Unlock()
		}
	}()

	// Generous relative to the caller's ctx (well above
	// observeTaskRunPublishTimeout) so the assertion below is measuring
	// PublishTaskRun's own internal timeout, not this test's deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err = PublishTaskRun(ctx, workDir, observe.EventTaskRunStarted, observe.TaskRun{
		ID:   "hang-1",
		Task: "hang",
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the daemon never responds")
	}
	// Lower bound proves this isn't the instant-ENOENT case; upper bound
	// proves observeTaskRunPublishTimeout — not ctx's 30s deadline — is what
	// actually bounded the call.
	if elapsed < observeTaskRunPublishTimeout/2 {
		t.Fatalf("PublishTaskRun returned in %v — too fast to have been bounded by observeTaskRunPublishTimeout (%v)", elapsed, observeTaskRunPublishTimeout)
	}
	if elapsed > observeTaskRunPublishTimeout+3*time.Second {
		t.Fatalf("PublishTaskRun took %v against a daemon that never responds — must be bounded by observeTaskRunPublishTimeout (%v), not ctx's deadline", elapsed, observeTaskRunPublishTimeout)
	}
}
