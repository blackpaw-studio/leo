package consult

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func waitingNotices(t *testing.T, d *Dispatcher, id string) map[string]Notification {
	t.Helper()
	notices := map[string]Notification{}
	for key, n := range mustGet(t, d, id).Notifications {
		if strings.HasPrefix(key, id+waitingNoticeInfix) {
			notices[key] = n
		}
	}
	return notices
}

func sweepAt(d *Dispatcher, now *time.Time, at time.Time) {
	advanceClock(now, at.Sub(*now))
	d.Sweep(at)
}

// A run waiting with no hook activity for the notice window tells its caller
// once, naming the run, what it waits on, how long it has been quiet and the
// ways out; later sweeps in the same quiet stretch add nothing.
func TestWaitingNoticeFiresOnceAfterQuiet(t *testing.T) {
	start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	now := start
	d, _, delivery, id := startNotifyingWaiting(t, &now, "the loop is running")
	sweepAt(d, &now, start.Add(waitingNoticeAfter-time.Minute))
	if got := waitingNotices(t, d, id); len(got) != 0 || len(delivery.calls) != 0 {
		t.Fatalf("notice before the window: %v calls=%q", got, delivery.calls)
	}
	sweepAt(d, &now, start.Add(waitingNoticeAfter))
	if got := waitingNotices(t, d, id); len(got) != 1 || len(delivery.calls) != 1 {
		t.Fatalf("notices=%v calls=%q", got, delivery.calls)
	}
	for _, want := range []string{"dispatch " + id + " (looper)", "waiting on 1 shell", "quiet 30m", "leo_release", "leo_cancel"} {
		if !strings.Contains(delivery.calls[0], want) {
			t.Fatalf("notice %q lacks %q", delivery.calls[0], want)
		}
	}
	for _, at := range []time.Duration{waitingNoticeAfter + time.Minute, 4 * time.Hour} {
		sweepAt(d, &now, start.Add(at))
	}
	if got := waitingNotices(t, d, id); len(got) != 1 || len(delivery.calls) != 1 {
		t.Fatalf("a later sweep repeated the notice: %v calls=%q", got, delivery.calls)
	}
	if rec := mustGet(t, d, id); rec.Status != StatusWaiting {
		t.Fatalf("the notice changed the run: status=%s", rec.Status)
	}
}

// Hook activity ends a quiet stretch, so the next one notifies again.
func TestWaitingNoticeRearmsAfterActivity(t *testing.T) {
	start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	now := start
	d, _, delivery, id := startNotifyingWaiting(t, &now, "the loop is running")
	sweepAt(d, &now, start.Add(waitingNoticeAfter))
	advanceClock(&now, 10*time.Minute)
	reportAll(t, d, id, claudeHook(t, "pre-1", "PreToolUse", ""))
	sweepAt(d, &now, start.Add(40*time.Minute+waitingNoticeAfter-time.Minute))
	if got := waitingNotices(t, d, id); len(got) != 1 {
		t.Fatalf("notified before the new stretch reached the window: %v", got)
	}
	sweepAt(d, &now, start.Add(40*time.Minute+waitingNoticeAfter))
	if got := waitingNotices(t, d, id); len(got) != 2 || len(delivery.calls) != 2 {
		t.Fatalf("notices=%v calls=%q", got, delivery.calls)
	}
}

// A caller that asked for no notifications, or is already blocked in a
// leo_wait on the run, gets no waiting notice.
func TestWaitingNoticeSuppressedWithoutNotify(t *testing.T) {
	t.Run("notify off", func(t *testing.T) {
		start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
		now := start
		off := false
		d, _, delivery, id := startNotifyingWaiting(t, &now, "the loop is running", func(r *Request) { r.Notify = &off })
		sweepAt(d, &now, start.Add(waitingNoticeAfter))
		requireSuppressedWaitingNotice(t, d, delivery, id)
	})
	t.Run("covered by a wait", func(t *testing.T) {
		start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
		now := start
		d, _, delivery, id := startNotifyingWaiting(t, &now, "the loop is running")
		ctx, cancel := context.WithCancel(context.Background())
		waited := make(chan struct{})
		go func() { defer close(waited); d.Wait(ctx, []string{id}, time.Hour) }()
		for !waitRegistered(d, id) {
			runtime.Gosched()
		}
		sweepAt(d, &now, start.Add(waitingNoticeAfter))
		cancel()
		<-waited
		requireSuppressedWaitingNotice(t, d, delivery, id)
	})
}

func waitRegistered(d *Dispatcher, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waitCoversRunLocked(d.runs[id].record)
}

func requireSuppressedWaitingNotice(t *testing.T, d *Dispatcher, delivery *fakeNotificationDelivery, id string) {
	t.Helper()
	got := waitingNotices(t, d, id)
	if len(got) != 1 || len(delivery.calls) != 0 {
		t.Fatalf("notices=%v calls=%q", got, delivery.calls)
	}
	for _, n := range got {
		if n.Disposition != NotificationSuppressed {
			t.Fatalf("notice=%+v", n)
		}
	}
}

// A waiting notice carries no turn result and is not a result delivered:
// it never collects or releases the run, even one set to release on finish
// whose turn has since ended.
func TestWaitingNoticeDeliveredHeaderOnlyAndDoesNotRelease(t *testing.T) {
	start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	now := start
	on := true
	d, rt, delivery, id := startNotifyingWaiting(t, &now, "the loop is running", func(r *Request) { r.ReleaseOnFinish = &on })
	sweepAt(d, &now, start.Add(waitingNoticeAfter))
	if len(delivery.calls) != 1 || strings.Contains(delivery.calls[0], inlineBegin) || strings.Contains(delivery.calls[0], "the loop is running") {
		t.Fatalf("calls=%q", delivery.calls)
	}
	if rec := mustGet(t, d, id); rec.Status != StatusWaiting || rt.killCount() != 0 {
		t.Fatalf("delivery moved the run: status=%s kills=%d", rec.Status, rt.killCount())
	}
	// The bridge acks the notice only after the run went idle: still not
	// this turn's result, so nothing is released.
	reportAll(t, d, id, taskStopFailed(t, "e9", "TaskStop", "task_id", "b1", "No task found with ID: b1"))
	rec := mustGet(t, d, id)
	if rec.Status != StatusIdle {
		t.Fatalf("setup: status=%s", rec.Status)
	}
	for key := range waitingNotices(t, d, id) {
		d.collectAcked(rec, key)
	}
	if rec := mustGet(t, d, id); rec.Status != StatusIdle || rt.killCount() != 0 {
		t.Fatalf("a waiting notice's ack released the run: status=%s kills=%d", rec.Status, rt.killCount())
	}
}
