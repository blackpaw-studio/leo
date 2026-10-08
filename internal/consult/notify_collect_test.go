package consult

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
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

// bridgedIsolatedRun starts a finished worktree-isolated run whose caller is
// the bridged agent "orch", with a delivery that queues over its bridge.
func bridgedIsolatedRun(t *testing.T) (*Dispatcher, *bridgedCaller, Record) {
	t.Helper()
	repo := gitTestRepo(t)
	d := worktreeDispatcher(t, t.TempDir(), "")
	rec := startIsolatedNotifying(t, d, repo, "claude")
	d.mu.Lock()
	d.runs[rec.ID].record.Caller, d.runs[rec.ID].record.CallerPaneID = "orch", "%4"
	d.mu.Unlock()
	c := newBridgedCaller(t, "orch", "%4")
	d.SetNotificationDelivery(c.delivery(&fakeNotificationDelivery{ready: true}))
	return d, c, rec
}

func ackBridge(t *testing.T, c *bridgedCaller, id string, ok bool) {
	t.Helper()
	if err := c.hub.Apply("orch", notifyLaunch, bridge.Report{Type: bridge.ReportAck, ID: id, OK: ok, Error: "no"}); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBridgedInlineDeliveryCollectsOnlyWhenTheModAcks(t *testing.T) {
	d, c, rec := bridgedIsolatedRun(t)
	d.SweepNotifications(context.Background())
	cmd := c.next(t)
	if got, _ := d.Get(rec.ID); got.WorktreeState == WorktreeRemoved || !worktreePresent(got) {
		t.Fatal("collected when the deliver was only queued")
	}
	ackBridge(t, c, cmd.ID, true)
	eventually(t, "collection after the ok ack", func() bool { got, _ := d.Get(rec.ID); return got.WorktreeState == WorktreeRemoved })
	if entry := d.Wait(context.Background(), []string{rec.ID}, RunTimeout)[0]; entry.Text != "ok" || entry.Err != "" {
		t.Fatalf("leo_wait after the ack = %+v", entry)
	}
}

func TestBridgedInlineDeliveryRejectedByTheModDoesNotCollect(t *testing.T) {
	d, c, rec := bridgedIsolatedRun(t)
	d.SweepNotifications(context.Background())
	ackBridge(t, c, c.next(t).ID, false)
	time.Sleep(20 * time.Millisecond) // a collection would be asynchronous: give it the chance
	if got, _ := d.Get(rec.ID); got.WorktreeState == WorktreeRemoved || !worktreePresent(got) {
		t.Fatalf("a rejected deliver collected the run (state %q)", got.WorktreeState)
	}
}

// --- a follow-up racing the collection ---

func isolatedSessionDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	d.ProcessCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1 1 0\n")
	}
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "ps" {
			return exec.CommandContext(ctx, name, args...)
		}
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
	}
	return d
}

func pendingItem(t *testing.T, d *Dispatcher, id string) pendingNotification {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.runs[id]
	return pendingNotification{state: s, record: cloneRecord(s.record), key: id + "#1"}
}

// The caller got the inline result and sent a follow-up before collection
// ran: the continuation takes the run's still-present worktree, and the late
// collection of the first turn then leaves it alone.
func TestFollowUpBeforeCollectionKeepsTheWorktree(t *testing.T) {
	repo := gitTestRepo(t)
	d := isolatedSessionDispatcher(t)
	rec := startIsolatedNotifying(t, d, repo, "claude")
	item := pendingItem(t, d, rec.ID)
	if rec.WorktreeState == WorktreeRemoved {
		t.Fatal("setup: already collected")
	}
	sent, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "follow up")
	if err != nil {
		t.Fatalf("a follow-up on a finished run whose worktree is still present: %v", err)
	}
	d.collectDelivered(context.Background(), item)
	got, _ := d.Get(rec.ID)
	if got.WorktreeState == WorktreeRemoved || !worktreePresent(got) {
		t.Fatalf("the first turn's collection removed a worktree the follow-up uses (state %q)", got.WorktreeState)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, RunTimeout) // let the follow-up finish before the temp dirs go
}

// Collection won the race: the follow-up recreates the removed worktree.
func TestFollowUpAfterCollectionRecreatesTheWorktree(t *testing.T) {
	repo := gitTestRepo(t)
	d := isolatedSessionDispatcher(t)
	rec := startIsolatedNotifying(t, d, repo, "claude")
	d.collectDelivered(context.Background(), pendingItem(t, d, rec.ID))
	if got, _ := d.Get(rec.ID); got.WorktreeState != WorktreeRemoved {
		t.Fatalf("setup: state %q", got.WorktreeState)
	}
	sent, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "follow up")
	if err != nil {
		t.Fatalf("a follow-up after collection: %v", err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, RunTimeout)
}

// A bridge claim a restart interrupted is queued again; the new deliver's ack
// collects the run like a first delivery's.
func TestRedeliveredBridgeClaimCollectsOnceAcked(t *testing.T) {
	d, c, rec := bridgedIsolatedRun(t)
	key := rec.Turns[len(rec.Turns)-1].TurnID
	d.mu.Lock()
	s := d.runs[rec.ID]
	n := s.record.Notifications[key]
	n.Disposition, n.Transport, n.ClaimedAt = NotificationClaimed, NotificationTransportBridge, d.now()
	s.record.Notifications[key] = n
	d.mu.Unlock()
	swept := make(chan struct{})
	go func() { d.SweepNotifications(context.Background()); close(swept) }()
	select {
	case <-swept:
	case <-time.After(5 * time.Second):
		t.Fatal("SweepNotifications hung redelivering a bridge claim")
	}
	ackBridge(t, c, c.next(t).ID, true)
	eventually(t, "collection after the redelivered deliver's ack", func() bool { got, _ := d.Get(rec.ID); return got.WorktreeState == WorktreeRemoved })
}
