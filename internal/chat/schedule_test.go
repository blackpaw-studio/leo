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
