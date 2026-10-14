package consult

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// heldAliveExec stubs the harness with a process that stays running after
// Start until release closes its stdin, then prints a finished result.
func heldAliveExec(t *testing.T, d *Dispatcher) (release func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = w.Close() }) }
	t.Cleanup(func() { release(); _ = r.Close() })
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", `read _; printf %s '{"type":"result","session_id":"sid-1","result":"ok","is_error":false,"usage":{"input_tokens":1,"output_tokens":1},"num_turns":1}'`)
		cmd.Stdin = r
		return cmd
	}
	return release
}

type sendOutcome struct {
	res SendResult
	err error
}

// sendAsync runs SendWithConfig on its own goroutine so a send that blocks
// for the life of the process fails the test instead of hanging it.
func sendAsync(d *Dispatcher, id, message string) <-chan sendOutcome {
	returned := make(chan sendOutcome, 2)
	go func() {
		res, err := d.SendWithConfig(context.Background(), testConfig(), id, message)
		returned <- sendOutcome{res, err}
	}()
	return returned
}

func expectDeliveredSend(t *testing.T, returned <-chan sendOutcome) SendResult {
	t.Helper()
	select {
	case got := <-returned:
		if got.err != nil || !got.res.Delivered || got.res.Queued {
			t.Fatalf("send = %+v, err %v, want delivered", got.res, got.err)
		}
		return got.res
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithConfig did not return while the started process was still running")
	}
	return SendResult{}
}

// TestHeadlessContinuationReturnsDeliveredWhileProcessRuns: the send returns
// once the process has started, not when it finishes.
func TestHeadlessContinuationReturnsDeliveredWhileProcessRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	release := heldAliveExec(t, d)

	sent := expectDeliveredSend(t, sendAsync(d, id, "again"))
	if rec, _ := d.Get(id); rec.Status != StatusRunning {
		t.Fatalf("status while process runs = %s, want running", rec.Status)
	}
	if turn := lastTurn(t, d, id); !turn.Delivered || turn.Outcome != "" {
		t.Fatalf("turn while process runs = %+v, want delivered and unresolved", turn)
	}

	release()
	if e := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]; e.Outcome != TurnFinished || !e.Delivered {
		t.Fatalf("continuation = %+v, want finished and delivered", e)
	}
}

// TestHeadlessContinuationCancelAfterStartKeepsDeliveredAndFreesSlot: a
// cancel while the started process runs interrupts a turn that stays
// delivered, and releases its slot.
func TestHeadlessContinuationCancelAfterStartKeepsDeliveredAndFreesSlot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	heldAliveExec(t, d)
	before := d.slots.InUse()

	returned := sendAsync(d, id, "again")
	sent := expectDeliveredSend(t, returned)
	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if e := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]; e.Outcome != TurnInterrupted || !e.Delivered {
		t.Fatalf("canceled started continuation = %+v, want interrupted and still delivered", e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.slots.InUse() > before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := d.slots.InUse(); got != before {
		t.Fatalf("slots in use = %d, want %d: the canceled turn's slot must be released", got, before)
	}
	select {
	case extra := <-returned:
		t.Fatalf("send returned a second result %+v", extra)
	default:
	}
}
