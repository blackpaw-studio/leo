package daemon

import (
	"context"
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
