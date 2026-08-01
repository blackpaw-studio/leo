package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// postObserveTaskRun posts raw JSON body directly to a running server's
// /observe/task-run route and returns the decoded envelope.
func postObserveTaskRun(t *testing.T, workDir string, body map[string]any) *Response {
	t.Helper()
	resp, err := Send(context.Background(), workDir, "POST", "/observe/task-run", body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return resp
}

// TestHandleObserveTaskRunDerivesStatusFromType is the regression test for
// finding #1: the handler used to store req.Run.Status verbatim (whatever a
// caller sent, including empty), when the wire contract promises Status is
// always one of exactly three values that agree with the event Type. This
// posts a task_run_started event with NO status field at all — matching a
// naive producer — and asserts the recorded run's Status is "running", not
// empty, and can never disagree with the type that was actually accepted.
func TestHandleObserveTaskRunDerivesStatusFromType(t *testing.T) {
	workDir := tmpWorkDir(t)
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil)
	s.SetObservability(bus, runLog, nil, "v-test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	resp := postObserveTaskRun(t, workDir, map[string]any{
		"type": "task_run_started",
		"run": map[string]any{
			"id":   "status-derive-1",
			"task": "status-derive",
			// status deliberately omitted/wrong to prove the server, not the
			// caller, decides it.
			"status": "bogus",
		},
	})
	if !resp.OK {
		t.Fatalf("expected accepted, got error: %s", resp.Error)
	}

	recent := runLog.Recent(0)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recorded run, got %d: %+v", len(recent), recent)
	}
	if recent[0].Status != observe.RunRunning {
		t.Fatalf("expected Status derived as %q, got %q", observe.RunRunning, recent[0].Status)
	}
}

// TestHandleObserveTaskRunRejectsMissingIDOrTask is the other half of
// finding #1: an ID-less run collapses every such event into one RunLog slot
// (record matches by ID), and an empty Task breaks any consumer that
// displays or groups by it. Both must be rejected with 400 rather than
// forwarded.
func TestHandleObserveTaskRunRejectsMissingIDOrTask(t *testing.T) {
	workDir := tmpWorkDir(t)
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil)
	s.SetObservability(bus, runLog, nil, "v-test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	cases := []map[string]any{
		{"type": "task_run_started", "run": map[string]any{"id": "", "task": "t"}},
		{"type": "task_run_started", "run": map[string]any{"id": "i", "task": ""}},
	}
	for _, body := range cases {
		resp := postObserveTaskRun(t, workDir, body)
		if resp.OK {
			t.Fatalf("expected rejection for %+v, got accepted", body)
		}
	}

	if len(runLog.Recent(0)) != 0 {
		t.Fatalf("expected nothing recorded from rejected events, got %+v", runLog.Recent(0))
	}
}

// TestHandleObserveTaskRunCapsFieldLengths is the regression test for
// finding #2's field-cap half: an oversized Error/Workspace/Model/Harness
// string must be truncated before it is retained by the (bounded but still
// in-memory) RunLog and rebroadcast to every SSE subscriber, mirroring the
// discipline already applied to Action.Detail.
func TestHandleObserveTaskRunCapsFieldLengths(t *testing.T) {
	workDir := tmpWorkDir(t)
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil)
	s.SetObservability(bus, runLog, nil, "v-test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	// 50 KiB: comfortably under the whole-body size cap (so this test isolates
	// per-field truncation from the body-size rejection covered separately
	// below) but far over any sane cap for a single display-text field.
	hugeError := strings.Repeat("x", 50*1024)
	resp := postObserveTaskRun(t, workDir, map[string]any{
		"type": "task_run_failed",
		"run": map[string]any{
			"id":    "cap-1",
			"task":  "cap-task",
			"error": hugeError,
		},
	})
	if !resp.OK {
		t.Fatalf("expected accepted (truncation, not rejection), got error: %s", resp.Error)
	}

	recent := runLog.Recent(0)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recorded run, got %d", len(recent))
	}
	if len(recent[0].Error) >= len(hugeError) {
		t.Fatalf("expected Error truncated well below %d bytes, got %d", len(hugeError), len(recent[0].Error))
	}
}

// TestHandleObserveTaskRunRejectsOversizedBody is the regression test for
// finding #2's body-size half: this route has no http.MaxBytesReader, unlike
// every route on the web (TCP) listener, and its input is both retained (up
// to observe.MaxRecentRuns entries) and rebroadcast to every SSE subscriber
// — a giant body here is far more damaging than on a route whose input is
// discarded after one use.
func TestHandleObserveTaskRunRejectsOversizedBody(t *testing.T) {
	workDir := tmpWorkDir(t)
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil)
	s.SetObservability(bus, runLog, nil, "v-test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	// A 1 MiB error string alone dwarfs any reasonable whole-body cap, well
	// before the per-field truncation in the handler even runs.
	giant := strings.Repeat("y", 1<<20)
	resp := postObserveTaskRun(t, workDir, map[string]any{
		"type": "task_run_failed",
		"run": map[string]any{
			"id":    "oversized-1",
			"task":  "oversized-task",
			"error": giant,
		},
	})
	if resp.OK {
		t.Fatal("expected a 1 MiB body to be rejected by a whole-body size cap")
	}
	if len(runLog.Recent(0)) != 0 {
		t.Fatalf("expected nothing recorded from a rejected oversized body, got %+v", runLog.Recent(0))
	}
}
