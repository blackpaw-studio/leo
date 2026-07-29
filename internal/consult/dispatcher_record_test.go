package consult

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHandle struct {
	mu       sync.Mutex
	rec      Record
	written  bytes.Buffer
	statuses []Status
	closed   Status
	cause    error
}

func (h *fakeHandle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.written.Write(p)
}

func (h *fakeHandle) SetStatus(s Status) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses = append(h.statuses, s)
	return nil
}

func (h *fakeHandle) Close(s Status, cause error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed, h.cause = s, cause
	return nil
}

// handleState is a race-free view of what a fake handle recorded.
type handleState struct {
	opened   Record
	statuses []Status
	closed   Status
	cause    error
	written  string
}

func (h *fakeHandle) snapshot() handleState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return handleState{
		opened:   h.rec,
		statuses: append([]Status(nil), h.statuses...),
		closed:   h.closed,
		cause:    h.cause,
		written:  h.written.String(),
	}
}

type fakeRecorder struct{ opened chan *fakeHandle }

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{opened: make(chan *fakeHandle, 8)}
}

func (r *fakeRecorder) Open(rec Record) (Handle, error) {
	h := &fakeHandle{rec: rec}
	r.opened <- h
	return h, nil
}

func (r *fakeRecorder) waitOpened(t *testing.T) *fakeHandle {
	t.Helper()
	select {
	case h := <-r.opened:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("no consult was recorded")
		return nil
	}
}

func TestConsultRecordsStreamAndStatusTransitions(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"codex opinion","is_error":false}`)
	}
	result, err := d.Consult(context.Background(), testConfig(), Request{
		Caller: "leo", Template: "claude", Prompt: "what do you think?", Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Consult: %v", err)
	}

	state := rec.waitOpened(t).snapshot()
	opened := state.opened

	if !strings.HasPrefix(opened.ID, "c-") || len(opened.ID) != 10 {
		t.Errorf("id = %q, want c- plus 8 hex chars", opened.ID)
	}
	if result.ID != opened.ID {
		t.Errorf("result id = %q, want the recorded id %q", result.ID, opened.ID)
	}
	if opened.Status != StatusQueued {
		t.Errorf("status at open = %q, want queued", opened.Status)
	}
	if opened.Caller != "leo" || opened.Template != "claude" || opened.Harness != "claude" || opened.Model != "opus" {
		t.Errorf("record = %+v, want the resolved harness and model", opened)
	}
	if opened.Prompt != "what do you think?" {
		t.Errorf("prompt = %q, want the caller's prompt without the preamble", opened.Prompt)
	}
	if len(state.statuses) != 1 || state.statuses[0] != StatusRunning {
		t.Errorf("statuses = %v, want a single running transition", state.statuses)
	}
	if state.closed != StatusDone || state.cause != nil {
		t.Errorf("closed as %q (%v), want done", state.closed, state.cause)
	}
	if !strings.Contains(state.written, "codex opinion") {
		t.Errorf("recorded stream = %q, want the harness output", state.written)
	}
}

func TestConsultRecordsFailure(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	if _, err := d.Consult(context.Background(), testConfig(), Request{
		Template: "claude", Prompt: "q", Workspace: t.TempDir(),
	}); err == nil {
		t.Fatal("expected an execution failure")
	}

	state := rec.waitOpened(t).snapshot()
	if state.closed != StatusFailed {
		t.Errorf("closed as %q, want failed", state.closed)
	}
	if state.cause == nil {
		t.Error("failure recorded without a cause")
	}
}

func TestConsultRecordsEmptyOutputAsFailure(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}
	if _, err := d.Consult(context.Background(), testConfig(), Request{
		Template: "claude", Prompt: "q", Workspace: t.TempDir(),
	}); err == nil {
		t.Fatal("expected a no-output failure")
	}
	if state := rec.waitOpened(t).snapshot(); state.closed != StatusFailed {
		t.Errorf("closed as %q, want failed", state.closed)
	}
}

func TestConsultRecordsQueuedConsultsAndCancellation(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	for range maxConcurrent {
		d.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = d.Consult(ctx, testConfig(), Request{Template: "claude", Prompt: "q"})
	}()

	// The consult is recorded before it competes for a slot, so work
	// waiting behind the concurrency limit is visible.
	h := rec.waitOpened(t)
	state := h.snapshot()
	if state.opened.Status != StatusQueued {
		t.Errorf("status at open = %q, want queued", state.opened.Status)
	}
	if len(state.statuses) != 0 {
		t.Errorf("statuses = %v, want none while still queued", state.statuses)
	}

	cancel()
	<-done

	if state := h.snapshot(); state.closed != StatusCanceled {
		t.Errorf("closed as %q, want canceled", state.closed)
	}
}

func TestConsultRecordsDeadlineAsTimeout(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := d.Consult(ctx, testConfig(), Request{
		Template: "claude", Prompt: "q", Workspace: t.TempDir(),
	}); err == nil {
		t.Fatal("expected a timeout")
	}
	if state := rec.waitOpened(t).snapshot(); state.closed != StatusTimeout {
		t.Errorf("closed as %q, want timeout", state.closed)
	}
}

func TestConsultRecordsCancellationWhileRunning(t *testing.T) {
	rec := newFakeRecorder()
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = d.Consult(ctx, testConfig(), Request{Template: "claude", Prompt: "q", Workspace: t.TempDir()})
	}()

	h := rec.waitOpened(t)
	// Wait for the run to actually start before cancelling.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if state := h.snapshot(); len(state.statuses) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if state := h.snapshot(); state.closed != StatusCanceled {
		t.Errorf("closed as %q, want canceled", state.closed)
	}
}

// TestConsultWithoutRecorderIsUnchanged guards the nil-recorder path other
// callers rely on.
func TestConsultWithoutRecorderIsUnchanged(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"ok","is_error":false}`)
	}
	result, err := d.Consult(context.Background(), testConfig(), Request{
		Template: "claude", Prompt: "q", Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Consult: %v", err)
	}
	if !strings.Contains(result.Text, "ok") {
		t.Fatalf("result = %+v", result)
	}
}
