package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func dispatchRequest(t *testing.T) Request {
	t.Helper()
	return Request{Template: "claude", Prompt: "question", Cwd: t.TempDir()}
}

func TestStartReturnsBeforeRunExits(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `sleep 1; echo '{"type":"result","result":"done","is_error":false}'`)
	}

	startedAt := time.Now()
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Start took %s; expected it to return before the command exits", elapsed)
	}
	entries := d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
	if len(entries) != 1 || entries[0].Status != StatusDone || entries[0].Text != "done" {
		t.Fatalf("Wait = %+v", entries)
	}
}

// removeAllRetrying bounds-and-retries os.RemoveAll against a transient
// "directory not empty" from a file that briefly existed (or was still
// being unlinked) at the moment of removal. Go's own t.TempDir() cleanup
// only retries this class of error on Windows (see testing.removeAll /
// isWindowsRetryable in the standard library) — on Linux and macOS a single
// os.RemoveAll failure is fatal with no retry at all, so any environment
// jitter around a directory's last write (slow CI filesystem, a scheduler
// pause between a write completing and the next syscall observing it) has
// zero tolerance. Registering this via t.Cleanup BEFORE t.TempDir() is
// called (so it runs AFTER — Cleanup order is LIFO, and t.TempDir()
// registers its own cleanup at first use) pre-empties the directory with
// retries of our own, so the stdlib's single-shot RemoveAll on the parent
// has nothing left to trip over.
func removeAllRetrying(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for {
		if lastErr = os.RemoveAll(dir); lastErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("removeAllRetrying: giving up on %s: %v", dir, lastErr)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDispatchPersistsViewerWindowIDAfterCompletion(t *testing.T) {
	stateDir := t.TempDir()
	t.Cleanup(func() { removeAllRetrying(t, stateDir) })
	recorder := NewFileRecorder(stateDir)
	d := NewDispatcherWithOnStart(recorder, context.Background(), func(Record) string { return "@42" })
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if entries := d.Wait(context.Background(), []string{started.ID}, time.Second); len(entries) != 1 || entries[0].Status != StatusDone {
		t.Fatalf("Wait = %+v", entries)
	}
	rec, err := LoadOne(stateDir, started.ID)
	if err != nil {
		t.Fatalf("LoadOne: %v", err)
	}
	if rec.ViewerWindowID != "@42" {
		t.Fatalf("ViewerWindowID = %q, want @42", rec.ViewerWindowID)
	}
}

func TestWaitCollectsDoneDispatch(t *testing.T) {
	var collected []Record
	d := NewDispatcherWithOnStart(nil, context.Background(), nil, func(rec Record) {
		collected = append(collected, rec)
	})
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := d.Wait(context.Background(), []string{started.ID}, time.Second)
	if len(entries) != 1 || entries[0].Status != StatusDone {
		t.Fatalf("Wait = %+v", entries)
	}
	if len(collected) != 1 || collected[0].ID != started.ID || collected[0].Status != StatusDone {
		t.Fatalf("collected = %+v", collected)
	}
}

func TestNonIsolatedCollectDoesNotWaitForReaping(t *testing.T) {
	collected := make(chan Record, 1)
	d := NewDispatcherWithOnStart(nil, context.Background(), nil, func(rec Record) {
		collected <- rec
	})
	state := &runState{
		record: Record{ID: "d-collected-immediately", Mode: ModeHeadless, Status: StatusCanceled, StartedAt: time.Now(), EndedAt: time.Now()},
		handle: nopHandle{}, done: make(chan struct{}), cancel: func() {},
	}
	d.runs[state.record.ID] = state
	d.waitDoneHook = func(id string) {
		t.Fatalf("Collect waited for non-isolated run %q to reap", id)
	}

	d.Collect(state.record)
	select {
	case rec := <-collected:
		if rec.ID != state.record.ID {
			t.Fatalf("collected %q, want %q", rec.ID, state.record.ID)
		}
	default:
		t.Fatal("Collect returned without invoking collection hook")
	}
}

func TestWaitAggregatesMixedStatuses(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if strings.Contains(strings.Join(args, " "), "slow") {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 30")
		}
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	doneReq := dispatchRequest(t)
	done, err := d.Start(context.Background(), testConfig(), doneReq)
	if err != nil {
		t.Fatal(err)
	}
	slowReq := dispatchRequest(t)
	slowReq.Prompt = "slow"
	running, err := d.Start(context.Background(), testConfig(), slowReq)
	if err != nil {
		t.Fatal(err)
	}
	canceledReq := dispatchRequest(t)
	canceledReq.Prompt = "slow"
	canceled, err := d.Start(context.Background(), testConfig(), canceledReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Cancel(canceled.ID); err != nil {
		t.Fatal(err)
	}

	entries := d.Wait(context.Background(), []string{done.ID, running.ID, canceled.ID}, 100*time.Millisecond)
	if entries[0].Status != StatusDone || entries[1].Status != StatusRunning || entries[2].Status != StatusCanceled {
		t.Fatalf("Wait = %+v", entries)
	}
	if entries[1].Err != "" {
		t.Fatalf("running entry has error: %+v", entries[1])
	}
	_, _ = d.Cancel(running.ID)
}

func TestWaitTimeoutAndUnknownID(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := d.Wait(context.Background(), []string{started.ID, "d-missing"}, 20*time.Millisecond)
	if entries[0].Status != StatusRunning || entries[0].Err != "" {
		t.Fatalf("timeout entry = %+v", entries[0])
	}
	if entries[1].Err != "unknown dispatch d-missing" {
		t.Fatalf("unknown entry = %+v", entries[1])
	}
	if entries[1].Status != StatusUnknown {
		t.Fatalf("unknown status = %q", entries[1].Status)
	}
	_, _ = d.Cancel(started.ID)
}

func TestWaitTimeoutCompletionRaceLeavesResultCollectable(t *testing.T) {
	var collected []Record
	d := NewDispatcherWithOnStart(nil, context.Background(), nil, func(rec Record) {
		collected = append(collected, rec)
	})
	state := &runState{record: Record{ID: "d-race", Kind: "dispatch", Mode: ModeHeadless, Status: StatusRunning, StartedAt: time.Now()}, handle: nopHandle{}, done: make(chan struct{})}
	d.runs[state.record.ID] = state
	calls := 0
	d.now = func() time.Time {
		calls++
		if calls == 2 {
			d.complete(state, StatusDone, "finished", nil)
		}
		return time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := d.Wait(ctx, []string{state.record.ID}, time.Hour)[0]
	if first.Status != StatusRunning {
		t.Fatalf("first Wait = %+v, want running", first)
	}
	if len(collected) != 0 {
		t.Fatalf("timed-out running result was collected: %+v", collected)
	}
	second := d.Wait(context.Background(), []string{state.record.ID}, time.Second)[0]
	if second.Status != StatusDone || second.Text != "finished" {
		t.Fatalf("second Wait = %+v, want done text", second)
	}
	if len(collected) != 1 {
		t.Fatalf("collection count = %d, want 1", len(collected))
	}
}

func TestTerminateDoesNotOverwriteCompletedRecord(t *testing.T) {
	d := NewDispatcher(nil)
	state := &runState{
		record: Record{ID: "d-done", Status: StatusRunning, StartedAt: time.Now()},
		handle: nopHandle{}, done: make(chan struct{}), cancel: func() {},
	}
	d.runs[state.record.ID] = state

	// Simulate terminate's stale pre-lock observation, then complete while it
	// waits for the dispatcher lock. This is the natural-completion-vs-cancel
	// race without relying on scheduler timing.
	d.mu.Lock()
	state.record.Status = StatusDone
	state.record.EndedAt = time.Now()
	result := make(chan Record, 1)
	go func() { result <- d.terminateState(state, StatusCanceled) }()
	d.mu.Unlock()

	if rec := <-result; rec.Status != StatusDone {
		t.Fatalf("terminate status = %q, want done", rec.Status)
	}
}

func TestNonIsolatedTimeoutPublishesBeforeCancellation(t *testing.T) {
	d := NewDispatcher(nil)
	state := &runState{
		record: Record{ID: "d-timeout-order", Mode: ModeHeadless, Status: StatusRunning, StartedAt: time.Now()},
		handle: nopHandle{}, done: make(chan struct{}),
	}
	state.cancel = func() { d.complete(state, StatusCanceled, "", context.Canceled) }
	d.runs[state.record.ID] = state

	if got := d.terminateState(state, StatusTimeout); got.Status != StatusTimeout {
		t.Fatalf("terminateState status = %q, want timeout", got.Status)
	}
	if got, err := d.Get(state.record.ID); err != nil || got.Status != StatusTimeout {
		t.Fatalf("Get = %+v, %v; want timeout", got, err)
	}
}

func TestNonIsolatedCancelDoesNotWaitForReaping(t *testing.T) {
	d := NewDispatcher(nil)
	const id = "d-cancel-immediate"
	state := &runState{
		record: Record{ID: id, Mode: ModeHeadless, Status: StatusRunning, StartedAt: time.Now()},
		handle: nopHandle{}, done: make(chan struct{}),
		cancel: func() {
			d.mu.Lock()
			_, serialized := d.serial[id]
			d.mu.Unlock()
			if serialized {
				t.Fatal("non-isolated cancellation entered the reap/cleanup boundary")
			}
		},
	}
	d.runs[state.record.ID] = state

	rec, err := d.Cancel(state.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusCanceled {
		t.Fatalf("Cancel status = %q, want canceled", rec.Status)
	}
}

func TestPruneTerminalRunsKeepsDiskFallback(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	d := NewDispatcher(recorder)
	now := time.Now()
	for i := range RecordsKept + 1 {
		id := fmt.Sprintf("d-%02d", i)
		rec := Record{ID: id, Status: StatusDone, StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-time.Duration(i) * time.Second)}
		handle, err := recorder.Open(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Close(StatusDone, nil); err != nil {
			t.Fatal(err)
		}
		d.runs[id] = &runState{record: rec, handle: handle, done: make(chan struct{}), cancel: func() {}}
	}

	d.mu.Lock()
	d.pruneTerminalRunsLocked()
	d.mu.Unlock()
	if len(d.runs) != RecordsKept {
		t.Fatalf("in-memory runs = %d, want %d", len(d.runs), RecordsKept)
	}
	// d-20 is still retained on disk but was the oldest in-memory terminal
	// record, so lookup must transparently reload it.
	if _, ok := d.runs["d-20"]; ok {
		t.Fatal("oldest terminal run was not pruned from memory")
	}
	if rec, err := d.Get("d-20"); err != nil || rec.Status != StatusDone {
		t.Fatalf("Get disk fallback = %+v, %v", rec, err)
	}
	if entries := d.Wait(context.Background(), []string{"d-20"}, 0); len(entries) != 1 || entries[0].Status != StatusDone {
		t.Fatalf("Wait disk fallback = %+v", entries)
	}
}

func TestCancelMarksRecordCanceled(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := d.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusCanceled {
		t.Fatalf("status = %q", rec.Status)
	}
	if _, err := d.Cancel(started.ID); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
}

func TestUnlimitedDispatchIsCanceledByCancel(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("unlimited dispatch has a deadline")
		}
		return exec.CommandContext(ctx, "sleep", "30")
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}
	if got := d.Wait(context.Background(), []string{started.ID}, time.Second)[0].Status; got != StatusCanceled {
		t.Fatalf("status = %q, want canceled", got)
	}
}

func TestUnlimitedDispatchIsCanceledByParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	d := NewDispatcher(nil, parent)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("unlimited dispatch has a deadline")
		}
		return exec.CommandContext(ctx, "sleep", "30")
	}
	started, err := d.Start(context.Background(), testConfig(), dispatchRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if got := d.Wait(context.Background(), []string{started.ID}, time.Second)[0].Status; got != StatusCanceled {
		t.Fatalf("status = %q, want canceled", got)
	}
}

func TestMarkInterruptedMarksNonTerminalRecordsFailed(t *testing.T) {
	state := t.TempDir()
	fr := NewFileRecorder(state)
	h, err := fr.Open(Record{ID: "d-active", Status: StatusRunning, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetStatus(StatusRunning); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(fr)
	d.MarkInterrupted()
	rec, err := LoadOne(state, "d-active")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailed || rec.Error != "daemon restarted" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestStartValidationReturnsImmediately(t *testing.T) {
	d := NewDispatcher(nil)
	_, err := d.Start(context.Background(), testConfig(), Request{Template: "missing", Cwd: t.TempDir()})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v", err)
	}
	_, err = d.Start(context.Background(), testConfig(), Request{Template: "claude", Cwd: "relative"})
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v", err)
	}
}
