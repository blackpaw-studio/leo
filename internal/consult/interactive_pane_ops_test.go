package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Cancel can stop waiting on its kill before a slow launch publishes. The
// launch must still not leave the canceled run with a live pane, even when
// it publishes in the gap before Cancel settles the run.
func TestInteractiveCancelKillsPanePublishedBeforeSettle(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	d.paneOpWait = 20 * time.Millisecond
	rt := &fakeInteractiveRuntime{arm: true, empty: true, alive: true}
	launching, release := make(chan struct{}), make(chan struct{})
	rt.launchHook = func() { close(launching); <-release }
	d.SetInteractiveRuntime(rt)
	freeOne := fillSlots(d)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	freeOne()
	<-launching
	d.beforeCancelSettle = func() {
		close(release)
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			if rec, _ := d.Get(started.ID); rt.killCount() > 0 && rec.PaneID == "" {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	rec, _ := d.Cancel(started.ID)
	if rec.Status != StatusCanceled || rec.PaneID != "" {
		t.Fatalf("cancel returned status=%s pane=%q, want canceled without a live pane", rec.Status, rec.PaneID)
	}
	if rt.killCount() < 1 {
		t.Fatal("the pane published during cancellation was never killed")
	}
	time.Sleep(20 * time.Millisecond)
	if len(d.sem) != cap(d.sem)-1 {
		t.Fatalf("slots in use = %d, want the run's slot released exactly once", len(d.sem))
	}
}

// A user turn that starts while the pane is being hidden queues a rejoin;
// if that turn reaches a permission prompt before the rejoin runs (here,
// while the hide is relaying out the caller window), the pane stays hidden.
func TestInteractiveNeedsInputDuringHideLayoutStaysHidden(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	mover := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	rt := &layoutRuntime{movingRuntime: mover}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{
		Template: "codex", Name: "impl", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive,
		CallerPaneID: "%0", CallerSessionID: "leo-orch", CallerWindowID: "@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, mover.fakeInteractiveRuntime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mover.hideHook = func() { _ = d.Report(started.ID, hook(t, "UserPromptSubmit", "u")) }
	rt.layoutHook = func() {
		go d.RequestPermission(ctx, started.ID, permissionPayload(t, "Bash", map[string]any{"command": "ls"}), time.Minute)
		deadline := time.Now().Add(time.Second)
		for {
			if rec, _ := d.Get(started.ID); rec.Status == StatusNeedsInput {
				return
			}
			if time.Now().After(deadline) {
				t.Error("run never reached needs_input")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	waitForEvents(t, mover, "hide", 1)
	time.Sleep(100 * time.Millisecond)
	for _, event := range mover.log() {
		if strings.HasPrefix(event, "show") {
			t.Fatalf("events = %v, want a needs_input pane left hidden", mover.log())
		}
	}
	if rec, _ := d.Get(started.ID); rec.ViewerKind != viewerHidden {
		t.Fatalf("viewer kind = %q, want hidden", rec.ViewerKind)
	}
}

// A pane op wedged on tmux (here a hide) must not wedge a cancellation
// queued behind it: Cancel gives up waiting after paneOpWait, and its kill
// still runs once the wedged op returns.
func TestInteractiveCancelBoundsWaitBehindHungPaneOp(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	d.paneOpWait = 50 * time.Millisecond
	mover := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true, alive: true}}
	d.SetInteractiveRuntime(mover)
	started, err := d.Start(context.Background(), testConfig(), Request{
		Template: "codex", Name: "impl", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive,
		CallerPaneID: "%0", CallerSessionID: "leo-orch", CallerWindowID: "@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, mover.fakeInteractiveRuntime)
	hiding, release := make(chan struct{}), make(chan struct{})
	mover.hideHook = func() { close(hiding); <-release }
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	<-hiding
	canceled := make(chan Record, 1)
	go func() { rec, _ := d.Cancel(started.ID); canceled <- rec }()
	select {
	case rec := <-canceled:
		if rec.Status != StatusCanceled {
			t.Fatalf("status = %s, want canceled", rec.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel blocked behind a hung pane op")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for mover.killCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the canceled run's pane was never killed")
		}
		time.Sleep(time.Millisecond)
	}
	for {
		if rec, _ := d.Get(started.ID); rec.PaneID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the canceled run kept its pane id")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForSettledHidden waits for the pane to be recorded hidden with a hide
// as the last pane move, i.e. placement converged on hidden.
func waitForSettledHidden(t *testing.T, d *Dispatcher, rt *movingRuntime, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rec, _ := d.Get(id)
		moves := []string{}
		for _, e := range rt.log() {
			if strings.HasPrefix(e, "hide") || strings.HasPrefix(e, "show") {
				moves = append(moves, e)
			}
		}
		if rec.ViewerKind == viewerHidden && len(moves) > 0 && strings.HasPrefix(moves[len(moves)-1], "hide") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("viewer kind = %q, moves = %v; want the pane hidden again", rec.ViewerKind, moves)
		}
		time.Sleep(time.Millisecond)
	}
}

// A permission prompt that lands while a rejoin's join-pane is running
// must not leave the needs_input pane in the caller's window. The rejoin
// here is a user turn typed while the pane was being hidden.
func TestInteractiveNeedsInputDuringShowEndsHidden(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{
		Template: "codex", Name: "impl", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive,
		CallerPaneID: "%0", CallerSessionID: "leo-orch", CallerWindowID: "@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := started.ID
	waitForInjection(t, rt.fakeInteractiveRuntime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.hideHook = func() {
		rt.hideHook = nil
		_ = d.Report(id, hook(t, "UserPromptSubmit", "u"))
	}
	rt.showHook = func() {
		rt.showHook = nil
		go d.RequestPermission(ctx, id, permissionPayload(t, "Bash", map[string]any{"command": "ls"}), time.Minute)
		waitForStatus(t, d, id, StatusNeedsInput)
	}
	_ = d.Report(id, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(id, hook(t, "Stop", "a"))
	waitForEvents(t, rt, "show", 1)
	waitForSettledHidden(t, d, rt, id)
}

// An orchestrator turn that ends while a rejoin's join-pane is running must
// still get its pane hidden, though the record said hidden when it ended.
func TestInteractiveTurnEndingDuringShowEndsHidden(t *testing.T) {
	d, rt, id := startSplitCodex(t)
	waitForHidden(t, d, id)
	rt.showHook = func() {
		rt.showHook = nil
		d.mu.Lock()
		s := d.runs[id]
		d.closeTurnLocked(s, s.record.Turns[len(s.record.Turns)-1].TurnID, TurnRejected, "ended mid-show")
		d.mu.Unlock()
	}
	go func() { _, _ = d.Send(context.Background(), id, "next") }()
	waitForEvents(t, rt, "show", 1)
	waitForSettledHidden(t, d, rt, id)
}
