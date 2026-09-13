package consult

import (
	"context"
	"errors"
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
	_, _ = d.Cancel(started.ID)
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
