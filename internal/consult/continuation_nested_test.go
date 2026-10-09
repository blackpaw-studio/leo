package consult

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// newContinuableTree is a dispatcher on a file recorder whose launches stay
// running when their prompt says "stay" and otherwise finish at once with a
// resumable session.
func newContinuableTree(t *testing.T) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	// Registered after the recorder's TempDir, so it runs first: stop every
	// live run and let its recording settle before the directory goes.
	t.Cleanup(func() {
		for _, rec := range d.Records() {
			if !rec.Status.Terminal() {
				_, _ = d.Cancel(rec.ID)
			}
		}
		for _, rec := range d.Records() {
			d.waitDone(rec.ID)
		}
	})
	d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if strings.Contains(strings.Join(args, " "), "stay") {
			return exec.CommandContext(ctx, "sleep", "30")
		}
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
	}
	return d
}

func startPrompt(t *testing.T, d *Dispatcher, prompt string, parent string) Record {
	t.Helper()
	cfg := oneSlotConfig()
	cfg.Defaults.Dispatch.MaxConcurrent = nil
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: prompt, Cwd: t.TempDir(), Kind: "dispatch", ParentDispatchID: parent})
	if err != nil {
		t.Fatalf("Start(%q): %v", prompt, err)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestContinuingARestoredExemptChildLeavesThePoolBalanced(t *testing.T) {
	d := newContinuableTree(t)
	parent := startPrompt(t, d, "stay", "")
	child := startPrompt(t, d, "done", parent.ID)
	d.Wait(context.Background(), []string{child.ID}, 5*time.Second)

	// A restored run (notify.go) is rebuilt from its record alone.
	rec, err := d.Get(child.ID)
	if err != nil || !rec.SlotExempt {
		t.Fatalf("child record = %+v, %v; want a slot-exempt record", rec, err)
	}
	rec.Notifications = map[string]Notification{"k": {Disposition: NotificationPending}}
	d.mu.Lock()
	delete(d.runs, child.ID)
	d.mu.Unlock()
	d.restorePendingNotifications(rec)

	sent, err := d.SendWithConfig(context.Background(), oneSlotConfig(), child.ID, "continue")
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	waitUntil(t, "the continuation to release", func() bool { return d.exempt.InUse() == 0 })
	if got := d.slots.InUse(); got != 1 {
		t.Fatalf("shared pool in use = %d, want only the parent's 1: the exempt child's slot leaked or was over-released", got)
	}
}

func liveChildren(t *testing.T, d *Dispatcher, parent string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		startPrompt(t, d, "stay", parent)
	}
}

func TestContinuingADescendantPastTheRootsCapIsRejected(t *testing.T) {
	d := newContinuableTree(t)
	root := startPrompt(t, d, "stay", "")
	done := startPrompt(t, d, "done", root.ID)
	d.Wait(context.Background(), []string{done.ID}, 5*time.Second)
	liveChildren(t, d, root.ID, MaxLiveDescendantsPerRoot)

	_, err := d.SendWithConfig(context.Background(), oneSlotConfig(), done.ID, "continue")
	var invalid *ValidationError
	if err == nil || !errorsAs(err, &invalid) || !containsAll(err.Error(), "6", "descendants") {
		t.Fatalf("continuation past the cap = %v, want the descendant-limit ValidationError", err)
	}
	if rec, _ := d.Get(done.ID); len(rec.Turns) != 1 || !rec.Status.Terminal() {
		t.Fatalf("a rejected continuation mutated the record: turns=%d status=%s", len(rec.Turns), rec.Status)
	}
}

func TestConcurrentContinuationsAdmitExactlyUpToTheCap(t *testing.T) {
	d := newContinuableTree(t)
	root := startPrompt(t, d, "stay", "")
	var finished []Record
	for i := 0; i < 5; i++ {
		rec := startPrompt(t, d, "done", root.ID)
		d.Wait(context.Background(), []string{rec.ID}, 5*time.Second)
		finished = append(finished, rec)
	}
	liveChildren(t, d, root.ID, MaxLiveDescendantsPerRoot-2) // room for exactly two more

	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for _, rec := range finished {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.SendWithConfig(context.Background(), oneSlotConfig(), rec.ID, "stay"); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 2 {
		t.Fatalf("admitted %d concurrent continuations, want exactly 2", admitted)
	}
}

func errorsAs(err error, target **ValidationError) bool { return errors.As(err, target) }
