package cron

import (
	"reflect"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// recorder captures runFn calls; spawn runs synchronously so tests need no
// waiting.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) run(_, _, taskName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, taskName)
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func newHoldScheduler(t *testing.T, tasks ...string) (*Scheduler, *recorder) {
	t.Helper()
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	rec := &recorder{}
	s.runFn = rec.run
	s.spawn = func(f func()) { f() }
	install(t, s, tasks...)
	return s, rec
}

func install(t *testing.T, s *Scheduler, tasks ...string) {
	t.Helper()
	cfg := &config.Config{Tasks: map[string]config.TaskConfig{}}
	for _, name := range tasks {
		cfg.Tasks[name] = config.TaskConfig{Schedule: "0 7 * * *", Enabled: true}
	}
	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
}

// fire runs name's cron job the way robfig would when its schedule hits.
func fire(t *testing.T, s *Scheduler, name string) {
	t.Helper()
	s.mu.Lock()
	id, ok := s.entries[name]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("task %q not scheduled", name)
	}
	s.cron.Entry(id).Job.Run()
}

func TestFireRunsTaskWhenNotHeld(t *testing.T) {
	s, rec := newHoldScheduler(t, "a")
	fire(t, s, "a")
	if got := rec.got(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("runs = %v, want [a]", got)
	}
}

func TestFireWhileHeldDefersTask(t *testing.T) {
	s, rec := newHoldScheduler(t, "a")
	s.Hold()
	fire(t, s, "a")
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("runs while held = %v, want none", got)
	}
	if got := s.Held(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Held = %v, want [a]", got)
	}
}

func TestReleaseRunsCoalescedFiringsInFirstFireOrder(t *testing.T) {
	s, rec := newHoldScheduler(t, "a", "b")
	s.Hold()
	fire(t, s, "b")
	fire(t, s, "a")
	fire(t, s, "b")
	if got := s.Held(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("Held = %v, want [b a]", got)
	}
	s.Release()
	if got := rec.got(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("runs after Release = %v, want [b a]", got)
	}
	if got := s.Held(); len(got) != 0 {
		t.Fatalf("Held after Release = %v, want empty", got)
	}
	fire(t, s, "a")
	if got := rec.got(); !reflect.DeepEqual(got, []string{"b", "a", "a"}) {
		t.Fatalf("firing after Release = %v, want it to run", got)
	}
}

func TestInstallDuringHoldKeepsHold(t *testing.T) {
	s, rec := newHoldScheduler(t, "a")
	s.Hold()
	install(t, s, "a", "b")
	fire(t, s, "b")
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("runs after reload while held = %v, want none", got)
	}
}

func TestReleaseSkipsTaskRemovedDuringHold(t *testing.T) {
	s, rec := newHoldScheduler(t, "a", "b")
	s.Hold()
	fire(t, s, "a")
	fire(t, s, "b")
	install(t, s, "b")
	s.Release()
	if got := rec.got(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("runs after Release = %v, want [b]", got)
	}
}

func TestRunNowRunsScheduledAndIgnoresUnknown(t *testing.T) {
	s, rec := newHoldScheduler(t, "a", "b")
	s.RunNow([]string{"ghost", "b", "a"})
	if got := rec.got(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("RunNow runs = %v, want [b a]", got)
	}
}
