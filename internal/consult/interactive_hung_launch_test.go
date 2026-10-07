package consult

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// A launch wedged on tmux must not wedge Cancel with it: Cancel gives up
// waiting after launchCancelWait, and the launch, when it finally returns,
// finds the run canceled and kills its own pane.
func TestInteractiveCancelBoundsWaitOnHungLaunch(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	d.launchCancelWait = 50 * time.Millisecond
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
		if rec.Status != StatusCanceled {
			t.Fatalf("status = %s, want canceled", rec.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel blocked on a hung launch")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for rt.killCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the late-launched pane of a canceled run was never killed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec, _ := d.Get(started.ID); rec.Status != StatusCanceled || rec.PaneID != "" {
		t.Fatalf("record status=%s pane=%q, want canceled without a pane", rec.Status, rec.PaneID)
	}
	time.Sleep(20 * time.Millisecond)
	if len(d.sem) != cap(d.sem)-1 {
		t.Fatalf("slots in use = %d, want the run's slot released exactly once", len(d.sem))
	}
}

func TestRuntimeViewerOverridesBoundsHungTmux(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.Timeout = 50 * time.Millisecond
	r.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	done := make(chan ViewerOverrides, 1)
	go func() { done <- r.ViewerOverrides(context.Background(), "leo-orch") }()
	select {
	case got := <-done:
		if got != (ViewerOverrides{}) {
			t.Fatalf("overrides = %+v, want none from a hung tmux", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ViewerOverrides blocked on a hung tmux show-options")
	}
}
