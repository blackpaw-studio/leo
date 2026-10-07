package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fillSlots occupies every concurrency slot and returns a function that frees
// one of them.
func fillSlots(d *Dispatcher) (freeOne func()) {
	for i := 0; i < cap(d.sem); i++ {
		d.sem <- struct{}{}
	}
	return func() { <-d.sem }
}

func (r *fakeInteractiveRuntime) launchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.placements)
}

func waitForLaunch(t *testing.T, r *fakeInteractiveRuntime) {
	t.Helper()
	deadline := time.After(time.Second)
	for r.launchCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("queued interactive dispatch never launched")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestInteractiveStartQueuesWhenLimiterFull(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	freeOne := fillSlots(d)

	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if !started.Queued || started.Pane != "" {
		t.Fatalf("started = %+v, want queued with no pane", started)
	}
	rec, _ := d.Get(started.ID)
	if rec.Status != StatusQueued || rec.PaneID != "" {
		t.Fatalf("record status=%s pane=%q, want queued without a pane", rec.Status, rec.PaneID)
	}
	if rt.launchCount() != 0 {
		t.Fatal("launched without a slot")
	}
	entry := d.Wait(context.Background(), []string{started.ID}, time.Millisecond)[0]
	if entry.Status != StatusQueued || entry.TurnID != started.ID+"#1" {
		t.Fatalf("wait while queued = %+v", entry)
	}

	freeOne()
	waitForLaunch(t, rt)
	waitForInjection(t, rt)
	rec, _ = d.Get(started.ID)
	if rec.PaneID != "%1" {
		t.Fatalf("pane = %q after launch", rec.PaneID)
	}
	if len(rec.Turns) != 1 || !rec.Turns[0].SlotHeld {
		t.Fatalf("turns = %+v, want one slot-holding opening turn", rec.Turns)
	}
	if len(d.sem) != cap(d.sem) {
		t.Fatalf("slots in use = %d, want the freed slot taken", len(d.sem))
	}
}

func TestInteractiveCancelWhileQueuedDoesNoPaneWork(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	freeOne := fillSlots(d)

	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := d.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusCanceled {
		t.Fatalf("status = %s, want canceled", rec.Status)
	}
	freeOne()
	time.Sleep(20 * time.Millisecond)
	if rt.launchCount() != 0 || rt.killCount() != 0 {
		t.Fatalf("launches=%d kills=%d, want no pane work", rt.launchCount(), rt.killCount())
	}
	if len(d.sem) != cap(d.sem)-1 {
		t.Fatalf("slots in use = %d, a canceled queued run must not take one", len(d.sem))
	}
}

func TestInteractiveSendWhileQueuedIsRejected(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	fillSlots(d)

	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Send(context.Background(), started.ID, "more")
	if err == nil || !strings.Contains(err.Error(), "queued") || !strings.Contains(err.Error(), "wait") {
		t.Fatalf("send while queued err = %v, want a queued/wait-first rejection", err)
	}
}
