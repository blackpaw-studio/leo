package consult

import (
	"context"
	"testing"
	"time"
)

// A cancel that lands after a queued run was admitted to launch must wait
// for the launch to publish, then tear the pane down; the slot is released
// exactly once.
func TestInteractiveCancelDuringQueuedLaunchWaitsAndKillsPane(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
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
	canceled := make(chan Record, 1)
	go func() { rec, _ := d.Cancel(started.ID); canceled <- rec }()
	select {
	case rec := <-canceled:
		t.Fatalf("cancel returned (%s) while the admitted launch was still in flight", rec.Status)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	var rec Record
	select {
	case rec = <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancel never returned")
	}
	if rec.Status != StatusCanceled || rec.PaneID != "" {
		t.Fatalf("record status=%s pane=%q, want canceled without a pane", rec.Status, rec.PaneID)
	}
	if rt.killCount() < 1 {
		t.Fatal("the launched pane of a canceled run was never killed")
	}
	time.Sleep(20 * time.Millisecond)
	if len(d.sem) != cap(d.sem)-1 {
		t.Fatalf("slots in use = %d, want the run's slot released exactly once", len(d.sem))
	}
}

// Claude can submit and finish its opening turn before Launch returns; the
// pane then publishes onto an idle run and must still be hidden.
func TestInteractiveOpeningIdleBeforePublishStillHides(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	rt.launchHook = func() {
		id := findSoleRunID(t, d)
		_ = d.Report(id, claudeHook(t, "submit-1", "UserPromptSubmit", dispatchPreamble+" hello"))
		_ = d.Report(id, claudeHook(t, "stop-1", "Stop", ""))
	}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{
		Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive,
		CallerPaneID: "%0", CallerSessionID: "leo-orch", CallerWindowID: "@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := d.Get(started.ID); rec.Status != StatusIdle {
		t.Fatalf("status = %s, want idle", rec.Status)
	}
	waitForEvents(t, rt, "hide", 1)
	waitForHidden(t, d, started.ID)
}

// A user turn starting while the pane is being broken out leaves the run
// running: the pane goes straight back where the user is working.
func TestInteractiveUserTurnDuringHideRejoinsPane(t *testing.T) {
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
	waitForInjection(t, rt.fakeInteractiveRuntime)
	rt.hideHook = func() { _ = d.Report(started.ID, hook(t, "UserPromptSubmit", "u")) }
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	waitForEvents(t, rt, "show", 1)
	if got := rt.log(); got[len(got)-1] != "show %1 %0 @1" {
		t.Fatalf("events = %v, want the pane rejoined after the racing user turn", got)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rec, _ := d.Get(started.ID)
		if rec.ViewerKind == "split" && rec.Status == StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("record kind=%q status=%s, want split and running", rec.ViewerKind, rec.Status)
		}
		time.Sleep(time.Millisecond)
	}
}
