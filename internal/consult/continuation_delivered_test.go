package consult

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHeadlessContinuationStartFailureIsAnErrorNotDelivered: a continuation
// whose process cannot start must fail the send, never report delivered.
func TestHeadlessContinuationStartFailureIsAnErrorNotDelivered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/leo-test-harness-binary")
	}
	slotsBefore := d.slots.InUse()

	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err == nil {
		t.Fatalf("SendWithConfig = %+v, want a start-failure error", sent)
	}
	if sent.Delivered || sent.TurnID != "" {
		t.Fatalf("failed send = %+v, want zero result", sent)
	}
	if !strings.Contains(err.Error(), "leo-test-harness-binary") {
		t.Fatalf("error %q does not name the failed start", err)
	}
	if turn := lastTurn(t, d, id); turn.Delivered || turn.Queued || turn.Outcome == "" {
		t.Fatalf("turn after failed start = %+v, want settled and undelivered", turn)
	}
	if got := d.slots.InUse(); got != slotsBefore {
		t.Fatalf("slots in use = %d, want %d: a failed start must release its slot", got, slotsBefore)
	}
}

// TestHeadlessContinuationReportsDeliveredOnlyAfterStart holds the launch
// just before cmd.Start and checks that nothing claims delivery yet.
func TestHeadlessContinuationReportsDeliveredOnlyAfterStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	atStart, atStartCh := signalCh()
	release := make(chan struct{})
	d.beforeHeadlessStart = func() {
		atStart()
		<-release
	}

	type outcome struct {
		res SendResult
		err error
	}
	returned := make(chan outcome, 1)
	go func() {
		res, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
		returned <- outcome{res, err}
	}()
	expectSignal(t, atStartCh, "launch reaching cmd.Start")

	select {
	case got := <-returned:
		t.Fatalf("SendWithConfig returned %+v before the process started", got)
	case <-time.After(50 * time.Millisecond):
	}
	if turn := lastTurn(t, d, id); turn.Delivered {
		t.Fatalf("turn before start = %+v, want undelivered", turn)
	}

	close(release)
	select {
	case got := <-returned:
		if got.err != nil || !got.res.Delivered || got.res.Queued {
			t.Fatalf("after start: %+v, err %v, want delivered", got.res, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithConfig did not return after start")
	}
	if turn := lastTurn(t, d, id); !turn.Delivered {
		t.Fatalf("turn after start = %+v, want delivered", turn)
	}
}
