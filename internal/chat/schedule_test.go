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
