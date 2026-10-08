package consult

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// startIsolatedNotifying starts a worktree-isolated headless run that
// notifies a caller of harness, and returns it once it has finished, with no
// leo_wait having touched it.
func startIsolatedNotifying(t *testing.T, d *Dispatcher, repo, harness string) Record {
	t.Helper()
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree", CallerPaneID: "%1", CallerHarness: harness, CallerSessionID: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	done := d.runs[started.ID].done
	d.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// turnKey is the notification key of a headless run's only turn.
func turnKey(rec Record) string { return rec.Turns[len(rec.Turns)-1].TurnID }

func worktreePresent(rec Record) bool {
	_, err := os.Stat(rec.Worktree)
	return err == nil
}

func TestInlineDeliveryCollectsAnIsolatedHeadlessRun(t *testing.T) {
	repo := gitTestRepo(t)
	d := worktreeDispatcher(t, t.TempDir(), "")
	rec := startIsolatedNotifying(t, d, repo, "claude")
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	got, err := d.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Notifications[turnKey(rec)].Disposition != NotificationDelivered || got.WorktreeState != WorktreeRemoved || worktreePresent(got) {
		t.Fatalf("after inline delivery: notification=%+v worktree state=%q present=%v", got.Notifications[turnKey(rec)], got.WorktreeState, worktreePresent(got))
	}
	// A later leo_wait still returns the stored result, without error or a
	// second cleanup.
	entry := d.Wait(context.Background(), []string{rec.ID}, RunTimeout)[0]
	if entry.Status != StatusDone || entry.Text != "ok" || entry.Err != "" {
		t.Fatalf("leo_wait after collection = %+v", entry)
	}
	if after, _ := d.Get(rec.ID); after.WorktreeState != WorktreeRemoved {
		t.Fatalf("worktree state after the wait = %q", after.WorktreeState)
	}
}

func TestInlineDeliveryKeepsADirtyWorktree(t *testing.T) {
	repo := gitTestRepo(t)
	d := worktreeDispatcher(t, t.TempDir(), "printf dirty > untracked.txt")
	rec := startIsolatedNotifying(t, d, repo, "claude")
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	got, _ := d.Get(rec.ID)
	if got.WorktreeState != WorktreeKept || !worktreePresent(got) {
		t.Fatalf("a dirty worktree must be kept: state=%q present=%v", got.WorktreeState, worktreePresent(got))
	}
}

func TestPointerOnlyDeliveryDoesNotCollect(t *testing.T) {
	repo := gitTestRepo(t)
	d := worktreeDispatcher(t, t.TempDir(), "")
	rec := startIsolatedNotifying(t, d, repo, "codex")
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	got, _ := d.Get(rec.ID)
	if got.Notifications[turnKey(rec)].Disposition != NotificationDelivered || got.WorktreeState == WorktreeRemoved || !worktreePresent(got) {
		t.Fatalf("a pointer-only delivery collected: notification=%+v state=%q", got.Notifications[turnKey(rec)], got.WorktreeState)
	}
	d.Wait(context.Background(), []string{rec.ID}, RunTimeout)
	if after, _ := d.Get(rec.ID); after.WorktreeState != WorktreeRemoved {
		t.Fatalf("leo_wait must still collect a pointer-only run, state = %q", after.WorktreeState)
	}
}

func TestFailedDeliveryDoesNotCollect(t *testing.T) {
	for name, err := range map[string]error{"ambiguous": ErrNotificationAmbiguous, "not sent": ErrNotificationNotSent, "other": errors.New("boom")} {
		repo := gitTestRepo(t)
		d := worktreeDispatcher(t, t.TempDir(), "")
		rec := startIsolatedNotifying(t, d, repo, "claude")
		d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true, err: err})
		d.SweepNotifications(context.Background())
		if got, _ := d.Get(rec.ID); got.WorktreeState == WorktreeRemoved || !worktreePresent(got) {
			t.Errorf("%s: a failed delivery collected the run (state %q)", name, got.WorktreeState)
		}
	}
}

func TestInlineDeliveryClosesTheViewerOfASuccessfulHeadlessRunOnce(t *testing.T) {
	d := NewDispatcher(nil)
	var collected []string
	d.onCollect = func(r Record) { collected = append(collected, r.ID) }
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	s := pendingDoneRun(d, "claude", "x")
	s.done = make(chan struct{})
	close(s.done)
	d.SweepNotifications(context.Background())
	d.Wait(context.Background(), []string{"d-1"}, 0)
	if len(collected) != 2 {
		t.Fatalf("collect ran %v; the notification and a later wait each run the (idempotent) collection", collected)
	}
}

func TestInlineDeliveryOfAnIdleInteractiveTurnCollectsNothing(t *testing.T) {
	d := NewDispatcher(nil)
	d.onCollect = func(r Record) { t.Fatalf("collected %s", r.ID) }
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	s := &runState{record: Record{ID: "d-1", Kind: "dispatch", Mode: ModeInteractive, Notify: true, CallerPaneID: "%1", CallerHarness: "claude", Status: StatusIdle,
		Turns:         []Turn{{TurnID: "d-1#1", Outcome: TurnFinished, Text: "hi"}},
		Notifications: map[string]Notification{"d-1#1": {Disposition: NotificationPending, Message: "line"}}}, handle: &durableTestHandle{}, done: make(chan struct{})}
	d.runs["d-1"] = s
	d.SweepNotifications(context.Background())
	if s.record.Notifications["d-1#1"].Disposition != NotificationDelivered {
		t.Fatal("not delivered")
	}
}

// A bridge claim a restart interrupted is redelivered, then collected: the
// collection takes the dispatcher lock, so the claim's bookkeeping must have
// released it first.
func TestRedeliveredBridgeClaimCollectsWithoutDeadlock(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	d := NewDispatcher(nil)
	collected := make(chan string, 2)
	d.onCollect = func(r Record) { collected <- r.ID }
	d.SetNotificationDelivery(c.delivery(&fakeNotificationDelivery{ready: true}))
	rec := notifyRecord()
	rec.Kind, rec.Status = "dispatch", StatusDone
	rec.Notifications = map[string]Notification{"d-7": {Disposition: NotificationClaimed, Transport: NotificationTransportBridge, ClaimedAt: d.now(), Message: "line"}}
	d.runs["d-7"] = &runState{record: rec, handle: &durableTestHandle{}, done: func() chan struct{} { c := make(chan struct{}); close(c); return c }()}
	swept := make(chan struct{})
	go func() { d.SweepNotifications(context.Background()); close(swept) }()
	select {
	case <-swept:
	case <-time.After(5 * time.Second):
		t.Fatal("SweepNotifications deadlocked collecting a redelivered bridge claim")
	}
	select {
	case <-collected:
	default:
		t.Fatal("the redelivered claim was not collected")
	}
}
