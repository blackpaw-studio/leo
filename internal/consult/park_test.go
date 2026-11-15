package consult

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// startParkable starts an interactive claude dispatch for a claude caller
// that is notified inline, in a git worktree when isolated, and finishes its
// opening turn with text. Its notice is left unswept.
func startParkable(t *testing.T, stateDir string, isolated bool, text string) (*Dispatcher, *paneRuntime, *fakeNotificationDelivery, string) {
	t.Helper()
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	return startParkableAt(t, stateDir, isolated, text, &now)
}

// startParkableAt is startParkable on a clock the test advances (under
// testClockMu).
func startParkableAt(t *testing.T, stateDir string, isolated bool, text string, now *time.Time) (*Dispatcher, *paneRuntime, *fakeNotificationDelivery, string) {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = sharedClock(now)
	rt := newPaneRuntime(true)
	rt.arm, rt.empty = true, true
	d.SetInteractiveRuntime(rt)
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	req := Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", Role: "implement", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1"}
	if isolated {
		req.Cwd, req.Isolation = gitTestRepo(t), "worktree"
	}
	started, err := d.Start(context.Background(), testConfig(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	if err := d.Report(started.ID, claudeHook(t, "submit-1", "UserPromptSubmit", "the opening brief")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, claudeStop(t, "stop-1", text, nil)); err != nil {
		t.Fatal(err)
	}
	return d, rt, delivery, started.ID
}

func requireParked(t *testing.T, d *Dispatcher, id, turnID string) Record {
	t.Helper()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(rec) || rec.PaneID != "" {
		t.Fatalf("run not parked: status=%s retained=%v pane=%q", rec.Status, rec.Retained, rec.PaneID)
	}
	if turnByID(rec, turnID).ResultCollectedAt.IsZero() {
		t.Fatalf("turn %s has no result_collected_at", turnID)
	}
	if _, ok := rec.Notifications[closeNoticeKey(id)]; ok {
		t.Fatalf("closing on delivery recorded a close notice: %+v", rec.Notifications)
	}
	return rec
}

func TestDeliveredResultParksPaneKeepsWorktree(t *testing.T) {
	stateDir := t.TempDir()
	d, rt, delivery, id := startParkable(t, stateDir, true, "the answer")

	d.SweepNotifications(context.Background())

	if len(delivery.calls) != 1 || !strings.Contains(delivery.calls[0], "the answer") {
		t.Fatalf("delivery = %q", delivery.calls)
	}
	if !slices.Equal(rt.killedPanes(), []string{"%1"}) {
		t.Fatalf("killed panes = %q", rt.killedPanes())
	}
	rec := requireParked(t, d, id, id+"#1")
	if rec.WorktreeState != WorktreePresent || !dirExists(rec.Worktree) {
		t.Fatalf("worktree state=%q exists=%v", rec.WorktreeState, dirExists(rec.Worktree))
	}
	disk, err := LoadOne(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(disk) || disk.WorktreeState != WorktreePresent {
		t.Fatalf("persisted: status=%s retained=%v worktree=%q", disk.Status, disk.Retained, disk.WorktreeState)
	}
	// A later sweep neither re-delivers nor sends a close notice.
	d.Sweep(time.Time{})
	if len(delivery.calls) != 1 {
		t.Fatalf("a parked run notified again: %q", delivery.calls)
	}
}

func TestWaitReturnedResultParksRun(t *testing.T) {
	d, rt, s := finishedRun(t, "codex", TurnFinished, true)
	s.record.Notify = false
	var entry Entry
	if err := d.WaitAndClose(context.Background(), []string{"d-rof"}, 0, func(e []Entry) error { entry = e[0]; return nil }); err != nil {
		t.Fatal(err)
	}
	if entry.Text != "the answer" {
		t.Fatalf("entry = %+v", entry)
	}
	if rt.killCount() != 1 {
		t.Fatalf("kills = %d", rt.killCount())
	}
	requireParked(t, d, "d-rof", "d-rof#1")
}

// TestSecondTurnDeliveryParksAgain: a run whose first result was too big to
// close on keeps its pane; the follow-up's complete result closes it.
func TestSecondTurnDeliveryParksAgain(t *testing.T) {
	d, rt, delivery, id := startParkable(t, t.TempDir(), false, strings.Repeat("x", maxInlineResultBytes+1))
	d.SweepNotifications(context.Background())
	if rec := status(d, id); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("a truncated result closed the pane: %s kills=%d", rec.Status, rt.killCount())
	}
	sent, err := d.Send(context.Background(), id, "a short follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Report(id, claudeHook(t, "submit-2", "UserPromptSubmit", "a short follow-up")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(id, claudeStop(t, "stop-2", "short answer", nil)); err != nil {
		t.Fatal(err)
	}
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 2 {
		t.Fatalf("delivery = %q", delivery.calls)
	}
	if rt.killCount() != 1 {
		t.Fatalf("kills = %d", rt.killCount())
	}
	requireParked(t, d, id, sent.TurnID)
}

func TestStaleTurnDeliveryDoesNotPark(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	// Turn 2 finished too, and only turn 1's notice is delivered now.
	s.record.Turns = append(s.record.Turns, Turn{TurnID: "d-rof#2", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "second"})
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	d.collectAcked(cloneRecord(s.record), "d-rof#1")
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("an older turn's result closed the pane: %s kills=%d", rec.Status, rt.killCount())
	}
}

func TestTypedTurnKeepsPane(t *testing.T) {
	d, rt, s := finishedRun(t, "claude", TurnFinished, true)
	s.record.Turns = []Turn{
		{TurnID: "d-rof#1", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "first"},
		{TurnID: "d-rof#2", Source: TurnSourceUser, Delivered: true, Outcome: TurnFinished, Text: "typed"},
		{TurnID: "d-rof#3", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "third"},
	}
	s.record.Notifications = map[string]Notification{"d-rof#3": {Disposition: NotificationPending, PendingAt: time.Unix(1000, 0), Message: "line"}}
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	d.SweepNotifications(context.Background())
	if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("a pane someone typed into was closed: %s kills=%d", rec.Status, rt.killCount())
	}
}

func TestReleaseOnFinishFalseKeepsPane(t *testing.T) {
	t.Run("inline delivery", func(t *testing.T) {
		d, rt, _ := finishedRun(t, "claude", TurnFinished, false)
		d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
		d.SweepNotifications(context.Background())
		if rec := status(d, "d-rof"); rec.Status != StatusIdle || rt.killCount() != 0 {
			t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
		}
	})
	t.Run("leo_wait still records the collection", func(t *testing.T) {
		d, rt, s := finishedRun(t, "claude", TurnFinished, false)
		s.record.Notify = false
		if err := d.WaitAndClose(context.Background(), []string{"d-rof"}, 0, func([]Entry) error { return nil }); err != nil {
			t.Fatal(err)
		}
		rec := status(d, "d-rof")
		if rec.Status != StatusIdle || rt.killCount() != 0 {
			t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
		}
		if turnByID(rec, "d-rof#1").ResultCollectedAt.IsZero() {
			t.Fatal("a complete leo_wait result was not recorded as collected")
		}
	})
}

func TestCollectRunSkipsParkedWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-parkwt", func(r *Record) {
		r.Status, r.Retained, r.WorktreeState, r.SessionID = StatusClosed, true, WorktreePresent, "sess-1"
	})
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	entry := d.Wait(context.Background(), []string{rec.ID}, time.Millisecond)[0]
	if entry.Status != StatusClosed {
		t.Fatalf("entry = %+v", entry)
	}
	got := d.Collect(rec)
	disk, err := LoadOne(stateDir, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreeState != WorktreePresent || disk.WorktreeState != WorktreePresent {
		t.Fatalf("parked worktree state: collect=%q disk=%q", got.WorktreeState, disk.WorktreeState)
	}
	if _, err := os.Stat(rec.Worktree); err != nil {
		t.Fatalf("parked worktree removed: %v", err)
	}
}
