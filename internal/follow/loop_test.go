package follow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// fakePuller counts pulls; block, when set, holds each pull until closed.
type fakePuller struct {
	mu      sync.Mutex
	pulls   int
	changed bool
	err     error
	block   chan struct{}
	started chan struct{}
}

func (f *fakePuller) Pull(ctx context.Context) (Outcome, error) {
	f.mu.Lock()
	f.pulls++
	block, started, out, err := f.block, f.started, Outcome{Changed: f.changed}, f.err
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	return out, err
}

func (f *fakePuller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls
}

// fakeTimer hands out one channel per After call and records the waits.
type fakeTimer struct {
	mu    sync.Mutex
	waits []time.Duration
	chans []chan time.Time
	asked chan time.Duration
}

func newFakeTimer() *fakeTimer { return &fakeTimer{asked: make(chan time.Duration, 16)} }

func (f *fakeTimer) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	ch := make(chan time.Time, 1)
	f.waits = append(f.waits, d)
	f.chans = append(f.chans, ch)
	f.mu.Unlock()
	f.asked <- d
	return ch
}

func (f *fakeTimer) fire() {
	f.mu.Lock()
	ch := f.chans[len(f.chans)-1]
	f.mu.Unlock()
	ch <- time.Time{}
}

func (f *fakeTimer) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.asked:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("loop never waited")
		return 0
	}
}

type loopFixture struct {
	path    string
	puller  *fakePuller
	timer   *fakeTimer
	reloads int
	mu      sync.Mutex
	loop    *Loop
}

func newLoopFixture(t *testing.T, yaml string) *loopFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leo.yaml")
	writeYAML(t, path, yaml)
	f := &loopFixture{path: path, puller: &fakePuller{}, timer: newFakeTimer()}
	f.loop = &Loop{
		Puller:     f.puller,
		LoadConfig: func() (*config.Config, error) { return config.Load(path) },
		Reload: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.reloads++
			return nil
		},
		Now:   func() time.Time { return time.Unix(0, 0) },
		After: f.timer.After,
	}
	return f
}

// waitPulls follows the loop's waits until it has pulled n times. A kick
// can be coalesced with an earlier one, so one wait is not enough.
func (f *loopFixture) waitPulls(t *testing.T, n int) {
	t.Helper()
	for range 10 {
		f.timer.next(t)
		if f.puller.count() >= n {
			if got := f.puller.count(); got != n {
				t.Fatalf("pulls = %d, want %d", got, n)
			}
			return
		}
		f.loop.Kick()
	}
	t.Fatalf("pulls = %d, want %d", f.puller.count(), n)
}

func (f *loopFixture) reloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads
}

func writeYAML(t *testing.T, path, yaml string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

const loopYAML = "client:\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\n    lead2: {ssh: evan@10.0.2.12}\nfollow:\n  host: lead\n  interval: 1m\n"

func runLoop(t *testing.T, f *loopFixture) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.loop.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestLoopPullsAtStartAndOnEachTick(t *testing.T) {
	f := newLoopFixture(t, loopYAML)
	runLoop(t, f)
	if d := f.timer.next(t); d != time.Minute || f.puller.count() != 1 {
		t.Fatalf("first wait %v after %d pulls; want 1m after 1", d, f.puller.count())
	}
	f.timer.fire()
	f.timer.next(t)
	if f.puller.count() != 2 {
		t.Fatalf("pulls = %d after a tick, want 2", f.puller.count())
	}
}

func TestLoopIntervalChangeTakesEffect(t *testing.T) {
	f := newLoopFixture(t, loopYAML)
	runLoop(t, f)
	f.timer.next(t)
	writeYAML(t, f.path, "client:\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n  interval: 2m\n")
	f.loop.Kick()
	if d := f.timer.next(t); d != 2*time.Minute {
		t.Fatalf("wait after an interval change = %v, want 2m", d)
	}
	if f.puller.count() != 1 {
		t.Fatalf("an interval change pulled (%d pulls); only a host change should", f.puller.count())
	}
}

func TestLoopKickPullsOnHostChangeOrFollowOn(t *testing.T) {
	f := newLoopFixture(t, "templates: {}\n")
	runLoop(t, f)
	f.loop.Kick()
	writeYAML(t, f.path, loopYAML)
	f.loop.Kick()
	f.waitPulls(t, 1)
	writeYAML(t, f.path, "client:\n  hosts:\n    lead2: {ssh: evan@10.0.2.12}\nfollow:\n  host: lead2\n")
	f.loop.Kick()
	f.waitPulls(t, 2)
}

func TestLoopReloadsOnlyOnChange(t *testing.T) {
	f := newLoopFixture(t, loopYAML)
	f.puller.changed = true
	runLoop(t, f)
	f.timer.next(t)
	if f.reloadCount() != 1 {
		t.Fatalf("reloads = %d after a changed pull, want 1", f.reloadCount())
	}
	f.puller.mu.Lock()
	f.puller.changed = false
	f.puller.mu.Unlock()
	f.timer.fire()
	f.timer.next(t)
	if f.reloadCount() != 1 {
		t.Fatalf("reloads = %d after an unchanged pull, want still 1", f.reloadCount())
	}
	f.puller.mu.Lock()
	f.puller.err = errors.New("unreachable")
	f.puller.mu.Unlock()
	f.timer.fire()
	f.timer.next(t)
	if f.reloadCount() != 1 {
		t.Fatal("a failed pull reloaded")
	}
}

func TestSyncNowSharesARunningPull(t *testing.T) {
	f := newLoopFixture(t, loopYAML)
	f.puller.changed = true
	f.puller.block = make(chan struct{})
	f.puller.started = make(chan struct{}, 4)
	results := make(chan error, 2)
	go func() { _, err := f.loop.SyncNow(context.Background()); results <- err }()
	<-f.puller.started
	go func() { _, err := f.loop.SyncNow(context.Background()); results <- err }()
	// Let the second caller join the flight before releasing it.
	for !f.loop.inFlightWaiters(2) {
		runtime.Gosched()
	}
	close(f.puller.block)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if f.puller.count() != 1 || f.reloadCount() != 1 {
		t.Fatalf("pulls = %d, reloads = %d; want one shared pull and one reload", f.puller.count(), f.reloadCount())
	}
}

func TestLoopWithoutFollowNeverPulls(t *testing.T) {
	f := newLoopFixture(t, "templates: {}\n")
	runLoop(t, f)
	f.loop.Kick()
	f.loop.Kick()
	if _, err := f.loop.SyncNow(context.Background()); !errors.Is(err, ErrNotFollowing) {
		t.Fatalf("SyncNow = %v, want ErrNotFollowing", err)
	}
	f.timer.mu.Lock()
	defer f.timer.mu.Unlock()
	if f.puller.count() != 0 || len(f.timer.waits) != 0 {
		t.Fatalf("pulls = %d waits = %v; want none", f.puller.count(), f.timer.waits)
	}
}

// inFlightWaiters reports whether the running pull has at least n callers.
func (l *Loop) inFlightWaiters(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flight != nil && l.flight.waiters >= n
}
