package consult

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestShutdownResolvesQueuedFollowUp: when the daemon context is canceled,
// a queued follow-up leaves the line and resolves interrupted; it never
// delivers afterwards, even if a slot frees.
func TestShutdownResolvesQueuedFollowUp(t *testing.T) {
	daemonCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	d := NewDispatcher(newFakeRecorder(), daemonCtx)
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	injected, injectedCh := signalCh()
	rt.injectHook = injected
	exited, exitedCh := signalCh()
	d.queuedSendExited = exited
	freeOne := fillSlots(d)
	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatal(err)
	}

	shutdown()
	expectSignal(t, exitedCh, "queued follow-up goroutine to leave the line")
	entry := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]
	if entry.Outcome != TurnInterrupted {
		t.Fatalf("wait on queued turn after shutdown = %+v, want outcome interrupted", entry)
	}
	turn := lastTurn(t, d, id)
	if turn.Queued || turn.SlotHeld || !strings.Contains(turn.Text, "shut") {
		t.Fatalf("resolved turn = %+v, want no queued/slot flags and a shutdown reason", turn)
	}
	if d.slots.Waiting() != 0 {
		t.Fatalf("%d waiters left in line after shutdown", d.slots.Waiting())
	}
	before := d.slots.InUse()
	freeOne()
	if d.slots.InUse() != before-1 {
		t.Fatalf("slots in use = %d, want the freed slot left free (was %d)", d.slots.InUse(), before)
	}
	expectNoSignal(t, injectedCh, "injection after shutdown")
}

// TestIdlessStopNeverFinishesAQueuedFollowUp: a late id-less Stop belongs to
// an earlier turn; the unsent queued follow-up must stay queued.
func TestIdlessStopNeverFinishesAQueuedFollowUp(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	id := idleInteractive(t, d)
	fillSlots(d)
	sent, err := d.Send(context.Background(), id, "more")
	if err != nil {
		t.Fatal(err)
	}

	_ = d.Report(id, hook(t, "Stop", ""))
	turn := lastTurn(t, d, id)
	if turn.TurnID != sent.TurnID || turn.Outcome != "" || !turn.Queued {
		t.Fatalf("queued turn after a late Stop = %+v, want it still open and queued", turn)
	}
	if d.slots.Waiting() != 1 {
		t.Fatalf("waiters in line = %d, want the follow-up to keep its place", d.slots.Waiting())
	}
}

// TestGrantRacingResolutionYieldsOneOutcome lands a cancel or a human steer
// in the gap between a queued follow-up being granted its slot and taking the
// dispatcher lock: it must resolve once, never inject, and not leak the slot.
func TestGrantRacingResolutionYieldsOneOutcome(t *testing.T) {
	cases := []struct {
		name    string
		resolve func(t *testing.T, d *Dispatcher, id string)
		outcome TurnOutcome
	}{
		{"cancel", func(t *testing.T, d *Dispatcher, id string) {
			if _, err := d.Cancel(id); err != nil {
				t.Fatal(err)
			}
		}, TurnInterrupted},
		{"steer", func(t *testing.T, d *Dispatcher, id string) {
			_ = d.Report(id, hookWithPrompt(t, "UserPromptSubmit", "u1", "typed by a human"))
		}, TurnLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(newFakeRecorder())
			rt := &fakeInteractiveRuntime{arm: true, empty: true}
			d.SetInteractiveRuntime(rt)
			id := idleInteractive(t, d)
			injected, injectedCh := signalCh()
			rt.injectHook = injected
			granted, grantedCh := signalCh()
			proceed := make(chan struct{})
			d.afterQueuedGrant = func() { granted(); <-proceed }
			exited, exitedCh := signalCh()
			d.queuedSendExited = exited
			freeOne := fillSlots(d)
			held := d.slots.InUse()
			sent, err := d.Send(context.Background(), id, "more")
			if err != nil {
				t.Fatal(err)
			}

			freeOne()
			expectSignal(t, grantedCh, "queued follow-up to be granted its slot")
			tc.resolve(t, d, id)
			close(proceed)
			expectSignal(t, exitedCh, "queued follow-up goroutine to return")

			expectNoSignal(t, injectedCh, "injection into a resolved run")
			rec, _ := d.Get(id)
			for _, turn := range rec.Turns {
				if turn.TurnID != sent.TurnID {
					continue
				}
				if turn.Outcome != tc.outcome || turn.Queued || turn.SlotHeld {
					t.Fatalf("raced turn = %+v, want outcome %s with no queued/slot flags", turn, tc.outcome)
				}
			}
			if got := d.slots.InUse(); got != held-1 {
				t.Fatalf("slots in use = %d, want %d (the granted slot must not leak or stay held)", got, held-1)
			}
			if d.slots.Waiting() != 0 {
				t.Fatalf("%d waiters left in line", d.slots.Waiting())
			}
		})
	}
}

// headlessExec stubs the harness process: each launch signals started and
// prints a finished result for session sid-1.
func headlessExec(d *Dispatcher, started func()) { headlessExecHeld(d, started, nil) }

// headlessExecHeld is headlessExec, except a non-nil release holds every
// launch (and so its slot) until it closes.
func headlessExecHeld(d *Dispatcher, started func(), release <-chan struct{}) {
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		started()
		if release != nil {
			<-release
		}
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false,"usage":{"input_tokens":1,"output_tokens":1},"num_turns":1}`)
	}
}

// TestHeadlessStartTakesItsPlaceInLineBeforeReturning: a follow-up sent right
// after a headless Start must queue behind it, not jump ahead.
func TestHeadlessStartTakesItsPlaceInLineBeforeReturning(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	id := idleInteractive(t, d)
	injected, injectedCh := signalCh()
	rt.injectHook = injected
	execd, execdCh := signalCh()
	release := make(chan struct{})
	defer close(release)
	headlessExecHeld(d, execd, release)
	freeOne := fillSlots(d)

	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "h", Cwd: t.TempDir(), Kind: "dispatch"}); err != nil {
		t.Fatal(err)
	}
	if got := d.slots.Waiting(); got != 1 {
		t.Fatalf("waiters after headless Start returned = %d, want its place already taken", got)
	}
	if _, err := d.Send(context.Background(), id, "follow-up"); err != nil {
		t.Fatal(err)
	}

	freeOne()
	expectSignal(t, execdCh, "headless run (first in line)")
	expectNoSignal(t, injectedCh, "follow-up injection ahead of the earlier headless start")
}

func finishedHeadless(t *testing.T, d *Dispatcher) string {
	t.Helper()
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "first", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	if e := d.Wait(context.Background(), []string{started.ID}, 5*time.Second)[0]; e.Outcome != TurnFinished {
		t.Fatalf("setup headless run = %+v", e)
	}
	return started.ID
}

// TestHeadlessContinuationQueuesWhenEverySlotIsBusy replaces the old
// "no capacity" rejection.
func TestHeadlessContinuationQueuesWhenEverySlotIsBusy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	freeOne := fillSlots(d)

	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err != nil {
		t.Fatalf("continuation with every slot busy = %v, want it queued", err)
	}
	if !sent.Queued || sent.TurnID != id+"#2" {
		t.Fatalf("send result = %+v, want queued turn %s#2", sent, id)
	}
	if sent.Delivered {
		t.Fatalf("send result = %+v, want not delivered while queued", sent)
	}
	if rec, _ := d.Get(id); rec.Status != StatusQueued {
		t.Fatalf("run status = %s, want queued", rec.Status)
	}
	if d.slots.Waiting() != 1 {
		t.Fatalf("waiters = %d, want the continuation in line", d.slots.Waiting())
	}
	if e := d.Wait(context.Background(), []string{sent.TurnID}, time.Millisecond)[0]; e.Status != StatusQueued || e.Delivered || e.Outcome != "" {
		t.Fatalf("wait while queued = %+v, want status queued, undelivered, no outcome", e)
	}

	freeOne()
	expectSignal(t, execdCh, "queued continuation launch")
	e := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]
	if e.Outcome != TurnFinished || !e.Delivered {
		t.Fatalf("queued continuation = %+v, want finished and delivered", e)
	}
}

func TestCancelWhileHeadlessContinuationQueuedLeavesTheLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	freeOne := fillSlots(d)
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if e := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]; e.Outcome != TurnInterrupted || e.Delivered {
		t.Fatalf("canceled queued continuation = %+v, want interrupted and never delivered", e)
	}
	if turn := lastTurn(t, d, id); turn.Queued || turn.SlotHeld {
		t.Fatalf("resolved turn = %+v, want no queued/slot flags", turn)
	}
	if d.slots.Waiting() != 0 {
		t.Fatalf("%d waiters left in line after cancel", d.slots.Waiting())
	}
	before := d.slots.InUse()
	freeOne()
	if d.slots.InUse() != before-1 {
		t.Fatalf("slots in use = %d, want the freed slot left free (was %d)", d.slots.InUse(), before)
	}
	expectNoSignal(t, execdCh, "launch of a canceled continuation")
}

// A cancel that lands after slot admission but before the process starts must
// not leave the turn reporting delivered: nothing was ever launched.
func TestCancelBetweenAdmissionAndStartNeverReportsDelivered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(newFakeRecorder())
	execd, execdCh := signalCh()
	headlessExec(d, execd)
	id := finishedHeadless(t, d)
	expectSignal(t, execdCh, "setup launch")
	freeOne := fillSlots(d)
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err != nil {
		t.Fatal(err)
	}
	inWindow := make(chan struct{})
	d.beforeHeadlessStart = func() {
		defer close(inWindow)
		if _, err := d.Cancel(id); err != nil {
			t.Error(err)
		}
	}

	before := d.slots.InUse()
	freeOne()
	expectSignal(t, inWindow, "admission-to-start window")
	e := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]
	if e.Outcome != TurnInterrupted || e.Delivered {
		t.Fatalf("turn canceled before start = %+v, want interrupted and undelivered", e)
	}
	if turn := lastTurn(t, d, id); turn.Queued || turn.Delivered {
		t.Fatalf("resolved turn = %+v, want neither queued nor delivered", turn)
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.slots.InUse() >= before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := d.slots.InUse(); got != before-1 {
		t.Fatalf("slots in use = %d, want %d: the canceled turn's slot must be released exactly once", got, before-1)
	}
}
