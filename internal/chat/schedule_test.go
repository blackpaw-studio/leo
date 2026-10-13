package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// rfc3339In is the fixture clock's now plus d, as an RFC3339 string.
func (f *harnessFixture) rfc3339In(d time.Duration) string {
	return f.clock.Now().Add(d).Format(time.RFC3339)
}

func (f *harnessFixture) schedule(t *testing.T, caller string, d time.Duration, text string) Schedule {
	t.Helper()
	r, err := f.svc.Schedule(context.Background(), ScheduleRequest{Caller: caller, Text: text, At: f.rfc3339In(d)})
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	return r
}

func TestScheduleValidation(t *testing.T) {
	f := newFixture(t)
	oneYear := f.clock.Now().AddDate(1, 0, 0)
	cases := []struct {
		name, caller, text, at string
		ok                     bool
	}{
		{"not a time", "assistant", "hi", "tomorrow", false},
		{"no offset", "assistant", "hi", "2026-10-13T09:00:00", false},
		{"exactly now", "assistant", "hi", f.clock.Now().Format(time.RFC3339), false},
		{"past", "assistant", "hi", f.clock.Now().Add(-time.Hour).Format(time.RFC3339), false},
		{"beyond a year", "assistant", "hi", oneYear.Add(time.Second).Format(time.RFC3339), false},
		{"empty text", "assistant", "  ", f.clock.Now().Add(time.Hour).Format(time.RFC3339), false},
		{"oversize text", "assistant", strings.Repeat("x", MaxTextBytes+1), f.clock.Now().Add(time.Hour).Format(time.RFC3339), false},
		{"unknown caller", "ghost", "hi", f.clock.Now().Add(time.Hour).Format(time.RFC3339), false},
		{"exactly a year", "assistant", "hi", oneYear.Format(time.RFC3339), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := f.svc.Schedule(context.Background(), ScheduleRequest{Caller: c.caller, Text: c.text, At: c.at})
			if c.ok {
				if err != nil || r.ID == "" || r.Agent != "assistant" || r.Kind != KindText {
					t.Fatalf("Schedule = %+v, %v", r, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if pending, _ := f.store.PendingSchedules("assistant"); len(pending) != 1 {
		t.Fatalf("rejected requests must store nothing; pending = %+v", pending)
	}
}

func TestScheduleFromATaskRunTargetsTheOwner(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	r, err := f.svc.Schedule(context.Background(), ScheduleRequest{Caller: "task:build", Task: "build", RunID: "r7", Text: "check the deploy", At: f.rfc3339In(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Agent != "web" || r.Kind != KindTask || r.Task != "build" || r.RunID != "r7" || r.Text != "check the deploy" {
		t.Fatalf("schedule = %+v", r)
	}
	if want := f.clock.Now().Add(time.Hour); !r.FireAt.Equal(want) {
		t.Fatalf("FireAt = %v, want %v", r.FireAt, want)
	}
	if got, _ := f.store.PendingSchedules("web"); len(got) != 1 || got[0].ID != r.ID {
		t.Fatalf("stored = %+v", got)
	}
	if ms := f.thread(t, "web"); len(ms) != 0 {
		t.Fatalf("scheduling posts nothing: %+v", ms)
	}
}

func TestReminderListIsScopedToTheCallersThread(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Owner: "web"}
	late := f.schedule(t, "web", 3*time.Hour, "late")
	early := f.schedule(t, "web", time.Hour, "early")
	f.schedule(t, "assistant", 2*time.Hour, "not web's")

	for _, req := range []ScheduleListRequest{
		{Caller: "web"},
		{Caller: "task:build", Task: "build", RunID: "r1"},
	} {
		got, err := f.svc.Schedules(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != early.ID || got[1].ID != late.ID {
			t.Fatalf("%+v lists %+v, want early then late", req, got)
		}
	}
	if _, err := f.svc.Schedules(context.Background(), ScheduleListRequest{Caller: "ghost"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown caller = %v, want ErrInvalid", err)
	}
}

func TestReminderCancelErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mine := f.schedule(t, "assistant", time.Hour, "mine")
	theirs := f.schedule(t, "web", time.Hour, "theirs")
	cancel := func(caller, id string) error {
		return f.svc.CancelSchedule(ctx, ScheduleCancelRequest{Caller: caller, ID: id})
	}

	for name, id := range map[string]string{"unknown": f.svc.opts.IDs.New(), "malformed": "../etc", "another thread's": theirs.ID} {
		if err := cancel("assistant", id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	if err := cancel("assistant", mine.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Schedules(ctx, ScheduleListRequest{Caller: "assistant"}); len(got) != 0 {
		t.Fatalf("cancelled reminder still listed: %+v", got)
	}
	if err := cancel("assistant", mine.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled twice = %v, want ErrConflict", err)
	}
	if ok, err := f.store.FireSchedule(theirs.ID, Message{ID: "M", Agent: "web", Direction: DirectionOut, Kind: KindText, TS: f.clock.Now()}, f.clock.Now()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := cancel("web", theirs.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("fired = %v, want ErrConflict", err)
	}
}

// recover starts the reminder loop, as the daemon does at startup.
func (f *harnessFixture) recover(t *testing.T) {
	t.Helper()
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
}

// nextArm is the duration of the loop's next timer arm.
func (f *harnessFixture) nextArm(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.timers.got:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the reminder loop never armed a timer")
		return 0
	}
}

func TestReminderFiresAtItsTime(t *testing.T) {
	f := newFixture(t)
	f.recover(t)
	r := f.schedule(t, "assistant", 2*time.Hour, "stand up")
	if d := f.nextArm(t); d != 2*time.Hour {
		t.Fatalf("armed %v, want 2h", d)
	}
	f.clock.Advance(2 * time.Hour)
	f.timers.fireLast()

	m := f.pub.waitMessage(t)
	if m.Agent != "assistant" || m.Direction != DirectionOut || m.Kind != KindText || m.Text != "stand up" || m.Task != "" {
		t.Fatalf("fired message = %+v", m)
	}
	if got, _ := f.store.PendingSchedules("assistant"); len(got) != 0 {
		t.Fatalf("still pending after firing %s: %+v", r.ID, got)
	}
	if ms := f.thread(t, "assistant"); len(ms) != 1 {
		t.Fatalf("thread = %+v, want the one reminder", texts(ms))
	}
}

func TestReminderRearmsForTheEarliest(t *testing.T) {
	f := newFixture(t)
	f.recover(t)
	f.schedule(t, "assistant", 2*time.Hour, "two")
	if d := f.nextArm(t); d != 2*time.Hour {
		t.Fatalf("first arm %v, want 2h", d)
	}
	soon := f.schedule(t, "assistant", time.Hour, "one")
	if d := f.nextArm(t); d != time.Hour {
		t.Fatalf("second arm %v, want 1h", d)
	}
	if err := f.svc.CancelSchedule(context.Background(), ScheduleCancelRequest{Caller: "assistant", ID: soon.ID}); err != nil {
		t.Fatal(err)
	}
	if d := f.nextArm(t); d != 2*time.Hour {
		t.Fatalf("arm after cancelling the earliest %v, want 2h", d)
	}
}

func TestCancelledReminderNeverFires(t *testing.T) {
	f := newFixture(t)
	f.recover(t)
	r := f.schedule(t, "assistant", time.Hour, "never")
	f.nextArm(t)
	if err := f.svc.CancelSchedule(context.Background(), ScheduleCancelRequest{Caller: "assistant", ID: r.ID}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Hour)
	f.timers.fireLast() // the stale timer of the cancelled reminder
	f.schedule(t, "assistant", 3*time.Hour, "later")
	if d := f.nextArm(t); d != 3*time.Hour {
		t.Fatalf("arm %v, want 3h", d)
	}
	if ms := f.thread(t, "assistant"); len(ms) != 0 {
		t.Fatalf("a cancelled reminder posted: %+v", texts(ms))
	}
}

func TestReminderFromATaskRunFiresAsATaskCard(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	f.recover(t)
	if _, err := f.svc.Schedule(context.Background(), ScheduleRequest{Caller: "task:build", Task: "build", RunID: "r7", Text: "check the deploy", At: f.rfc3339In(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	f.nextArm(t)
	f.clock.Advance(time.Hour)
	f.timers.fireLast()
	m := f.pub.waitMessage(t)
	if m.Agent != "web" || m.Kind != KindTask || m.Task != "build" || m.RunID != "r7" || m.Direction != DirectionOut || m.Text != "check the deploy" {
		t.Fatalf("fired message = %+v", m)
	}
}

func TestRestartPostsAPastReminderOnceWithTheLateFlag(t *testing.T) {
	for _, c := range []struct {
		name     string
		lateness time.Duration
		late     bool
	}{
		{"30s late", 30 * time.Second, false},
		{"exactly 60s late", 60 * time.Second, false},
		{"61s late", 61 * time.Second, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.recover(t)
			r := f.schedule(t, "assistant", time.Hour, "water the plants")
			f.nextArm(t)
			f.clock.Advance(time.Hour + c.lateness) // the daemon is down meanwhile

			wantText := "water the plants"
			if c.late {
				wantText += "\n\n_(late — scheduled for " + r.FireAt.Local().Format("Mon 2 Jan 2006 15:04 MST") + ")_"
			}
			for restart := 1; restart <= 2; restart++ {
				f.restart(t)
				f.recover(t)
				ms := f.thread(t, "assistant")
				if len(ms) != 1 || ms[0].Text != wantText || ms[0].Kind != KindText {
					t.Fatalf("after restart %d: thread = %q, want one %q", restart, texts(ms), wantText)
				}
			}
		})
	}
}

// failMessageInserts makes every message insert abort, as a full disk would,
// and returns the function that lifts the failure.
func failMessageInserts(t *testing.T, st *Store) (lift func()) {
	t.Helper()
	if _, err := st.db.Exec(`CREATE TRIGGER fail_inserts BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := st.db.Exec(`DROP TRIGGER fail_inserts`); err != nil {
			t.Fatal(err)
		}
	}
}

// A failing fire waits out the retry interval; it must not re-arm at zero
// and spin while the overdue reminder stays pending.
func TestFailedFireRetriesAfterTheRetryInterval(t *testing.T) {
	f := newFixture(t)
	f.recover(t)
	f.schedule(t, "assistant", time.Hour, "stand up")
	f.nextArm(t)
	lift := failMessageInserts(t, f.store)
	f.clock.Advance(time.Hour)
	f.timers.fireLast()

	if d := f.nextArm(t); d != scheduleRetry {
		t.Fatalf("armed %v after a failed fire, want the %v retry", d, scheduleRetry)
	}
	if got, _ := f.store.PendingSchedules("assistant"); len(got) != 1 {
		t.Fatalf("a failed fire must leave the reminder pending: %+v", got)
	}
	lift()
	f.clock.Advance(scheduleRetry)
	f.timers.fireLast()

	m := f.pub.waitMessage(t)
	if m.Text != "stand up" {
		t.Fatalf("retried message = %+v", m)
	}
	if ms := f.thread(t, "assistant"); len(ms) != 1 {
		t.Fatalf("thread = %q, want exactly one post", texts(ms))
	}
}

// A schedule or cancel kick wakes the loop out of its retry wait.
func TestKickInterruptsTheRetryWait(t *testing.T) {
	f := newFixture(t)
	f.recover(t)
	f.schedule(t, "assistant", time.Hour, "stand up")
	f.nextArm(t)
	lift := failMessageInserts(t, f.store)
	f.clock.Advance(time.Hour)
	f.timers.fireLast()
	if d := f.nextArm(t); d != scheduleRetry {
		t.Fatalf("armed %v, want the retry", d)
	}
	lift()
	f.schedule(t, "assistant", 3*time.Hour, "later") // kick; the retry timer never fires

	if m := f.pub.waitMessage(t); m.Text != "stand up" {
		t.Fatalf("message = %+v", m)
	}
}
