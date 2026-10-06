package bridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced Clock. Every After call is announced on
// armed, so a test can wait for a goroutine to reach its timeout select
// before advancing — synchronization by channel, never by sleeping.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
	armed  chan time.Duration
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:   time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
		armed: make(chan time.Duration, 1024),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	c.armed <- d
	return ch
}

// Advance moves time forward and fires every timer now due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var pending []fakeTimer
	for _, t := range c.timers {
		if t.at.After(c.now) {
			pending = append(pending, t)
			continue
		}
		t.ch <- c.now
	}
	c.timers = pending
}

// waitArmed blocks until some goroutine has called After.
func (c *fakeClock) waitArmed(t *testing.T) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a timer to be armed")
	}
}

// seqIDs returns a deterministic id generator: c1, c2, ...
func seqIDs() func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("c%d", n)
	}
}

// recorder is a Subscriber that keeps every event it is handed.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) OnBridgeEvent(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// testCtx bounds a test's blocking calls so a hang fails fast instead of
// stalling the whole suite.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// sendAsync runs Send in a goroutine and returns its eventual result.
func sendAsync(ctx context.Context, h *Hub, agent string, cmd Command) <-chan error {
	out := make(chan error, 1)
	go func() { out <- h.Send(ctx, agent, cmd) }()
	return out
}

// result waits for an async result, failing the test if it never comes.
func result(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for result")
		return nil
	}
}

// mustNext reads the next command or fails the test.
func mustNext(t *testing.T, s *Stream) Command {
	t.Helper()
	cmd, err := s.Next(testCtx(t))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return cmd
}

// testLaunch is the launch token the test helpers open keys under.
const testLaunch = "launch-test"

// mustOpen opens a new generation of agent's key under testLaunch.
func mustOpen(t *testing.T, h *Hub, agent string) Target {
	t.Helper()
	target, err := h.Open(agent, testLaunch)
	if err != nil {
		t.Fatalf("Open(%q): %v", agent, err)
	}
	return target
}

// launchOf returns the launch agent's current generation is bound to,
// opening the key under testLaunch first if the hub has never seen it, so
// tests about something else need no ceremony. A forgotten key stays
// forgotten.
func launchOf(t *testing.T, h *Hub, agent string) string {
	t.Helper()
	h.mu.Lock()
	life, ok := h.lives[agent]
	h.mu.Unlock()
	if !ok {
		mustOpen(t, h, agent)
		return testLaunch
	}
	if life.launch == "" {
		return testLaunch
	}
	return life.launch
}

func mustConnect(t *testing.T, h *Hub, agent string) *Stream {
	t.Helper()
	s, err := h.Connect(agent, launchOf(t, h, agent))
	if err != nil {
		t.Fatalf("Connect(%q): %v", agent, err)
	}
	return s
}

func ack(t *testing.T, h *Hub, agent, id string) {
	t.Helper()
	if err := h.Apply(agent, launchOf(t, h, agent), Report{Type: ReportAck, ID: id, OK: true}); err != nil {
		t.Fatalf("ack %s: %v", id, err)
	}
}
