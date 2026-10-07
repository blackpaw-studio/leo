package observe

import (
	"sort"
	"sync"
	"time"
)

// staticKeys resolves bridge keys from a fixed map.
type staticKeys map[string]string

func (k staticKeys) AgentForKey(key string) (string, bool) {
	name, ok := k[key]
	return name, ok
}

// fakeClock is a FeedClock driven by Advance: timers fire synchronously,
// in deadline order, as Advance passes them.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) (stop func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// Advance moves the clock forward by d, firing every timer due on the way
// with the clock set to its deadline.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var due *fakeTimer
		for i, t := range c.timers {
			if t.stopped {
				continue
			}
			if !t.at.After(end) {
				due = t
				c.timers = append(c.timers[:i:i], c.timers[i+1:]...)
			}
			break
		}
		if due == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		due.stopped = true
		c.now = due.at
		c.mu.Unlock()
		due.fn()
	}
}

// pendingTimers counts armed timers.
func (c *fakeClock) pendingTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// jump moves the clock forward by d without firing due timers, as when a
// timer goroutine has not run yet.
func (c *fakeClock) jump(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
