package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/run"
)

// TestTaskRunObservabilityReachesDaemonOverIPC is the regression test for the
// blocker: internal/run.Run only ever executes inside a `leo run` subprocess,
// never inside the daemon's own process, so run.SetPublisher(runLog) at
// daemon boot (see wireObservability) can never make a subprocess's
// task_run_* events visible — the package global it sets lives in a
// different OS process entirely.
//
// This drives the actual fix end-to-end from the producer side: it wires
// run.SetPublisher to a real daemon.ObservePublisher (the same call
// internal/cli/run.go makes before every invocation), publishes through it
// exactly as internal/run's producers do, and asserts the event lands in the
// *daemon's own* RunLog — the one wireObservability built and the one
// /api/v1/state and /api/v1/events actually read. Unlike
// TestObservabilityWiringEndToEnd (which calls runLog.Publish directly, i.e.
// pretends to be the daemon process itself), this test never touches the
// daemon's runLog handle directly — everything crosses the Unix socket.
func TestTaskRunObservabilityReachesDaemonOverIPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Short path under /tmp: macOS caps Unix socket paths at 104 chars, and
	// t.TempDir() nests too deep for that here.
	homePath, err := os.MkdirTemp("/tmp", "leo-ipc-*")
	if err != nil {
		t.Fatalf("creating temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(homePath) })
	if err := os.MkdirAll(filepath.Join(homePath, "state"), 0700); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}

	fakeTmux := writeFakeTmuxScript(t)
	sv := NewSupervisor(ctx)
	sv.tmuxPath = fakeTmux
	sv.homePath = homePath

	bus, runLog, messageLog, _ := wireObservability(ctx, sv, fakeTmux)

	srv := daemon.New(daemon.SockPath(homePath), filepath.Join(homePath, "leo.yaml"), nil)
	srv.SetObservability(bus, runLog, messageLog, nil, "v-ipc-test")
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown() }) //nolint:errcheck

	// This is the exact call internal/cli/run.go makes before run.Run/Preview.
	pub := daemon.NewObservePublisher(homePath)
	t.Cleanup(func() { run.SetPublisher(nil) })
	run.SetPublisher(pub)

	startedAt := time.Now()
	// Publish through the seam a subprocess's producer actually uses — the
	// Publisher set by run.SetPublisher — rather than the daemon's runLog
	// handle. run/observe_test.go already proves internal/run calls this
	// seam correctly with a recording fake; what's under test here is that
	// the seam's production target (daemon.ObservePublisher) really crosses
	// the socket into this daemon's own RunLog.
	pub.Publish(observe.Event{
		Type: observe.EventTaskRunStarted,
		Payload: &observe.TaskRunPayload{
			Run: observe.TaskRun{
				ID:        "ipc-task-1",
				Task:      "ipc-task",
				Status:    observe.RunRunning,
				StartedAt: startedAt,
			},
		},
	})

	deadline := time.After(5 * time.Second)
	for {
		found := false
		for _, r := range runLog.Recent(0) {
			if r.ID == "ipc-task-1" {
				found = true
			}
		}
		if found {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("run log %+v never saw ipc-task-1 — IPC publish path is broken", runLog.Recent(0))
		case <-time.After(50 * time.Millisecond):
		}
	}
}
