package consult

import (
	"sync"

	"github.com/blackpaw-studio/leo/internal/config"
)

// DefaultMaxConcurrent is the slot cap before any config is applied.
const DefaultMaxConcurrent = config.DefaultDispatchMaxConcurrent

// slotLimiter bounds concurrent orchestrator turns. Waiters are granted slots
// strictly in the order they enqueued, so queued new dispatches and queued
// follow-ups share one FIFO line. A max of zero or less means unlimited.
type slotLimiter struct {
	mu      sync.Mutex
	max     int
	used    int
	waiters []*slotWaiter
}

// slotWaiter is one place in line; Ready closes once the slot is granted.
type slotWaiter struct {
	ready   chan struct{}
	granted bool
	left    bool
}

func newSlotLimiter(max int) *slotLimiter { return &slotLimiter{max: max} }

// Ready closes when the waiter holds a slot.
func (w *slotWaiter) Ready() <-chan struct{} { return w.ready }

func (l *slotLimiter) hasRoomLocked() bool { return l.max <= 0 || l.used < l.max }

// grantLocked hands free slots to the head of the line.
func (l *slotLimiter) grantLocked() {
	for len(l.waiters) > 0 && l.hasRoomLocked() {
		w := l.waiters[0]
		l.waiters = l.waiters[1:]
		l.used++
		w.granted = true
		close(w.ready)
	}
}

// AcquireOrEnqueue takes a slot when one is free with nobody ahead
// (acquired true, no waiter); otherwise it joins the line atomically, so the
// caller's place is fixed at the moment the slots were found busy.
func (l *slotLimiter) AcquireOrEnqueue() (w *slotWaiter, acquired bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waiters) == 0 && l.hasRoomLocked() {
		l.used++
		return nil, true
	}
	w = &slotWaiter{ready: make(chan struct{})}
	l.waiters = append(l.waiters, w)
	return w, false
}

// TryAcquire takes a slot only when one is free and nobody is waiting ahead.
func (l *slotLimiter) TryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waiters) > 0 || !l.hasRoomLocked() {
		return false
	}
	l.used++
	return true
}

// Enqueue takes a place in line, granted at once when a slot is free.
func (l *slotLimiter) Enqueue() *slotWaiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := &slotWaiter{ready: make(chan struct{})}
	l.waiters = append(l.waiters, w)
	l.grantLocked()
	return w
}

// Cancel leaves the line. A slot already granted to w is released. It is
// idempotent, so any number of paths may cancel the same waiter.
func (l *slotLimiter) Cancel(w *slotWaiter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w.left {
		return
	}
	w.left = true
	if w.granted {
		l.releaseLocked()
		return
	}
	for i, q := range l.waiters {
		if q == w {
			l.waiters = append(l.waiters[:i:i], l.waiters[i+1:]...)
			return
		}
	}
}

// Release frees one slot. Releasing with none held is a no-op.
func (l *slotLimiter) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked()
}

func (l *slotLimiter) releaseLocked() {
	if l.used > 0 {
		l.used--
	}
	l.grantLocked()
}

// SetMax changes the cap; a raised cap admits waiters immediately.
func (l *slotLimiter) SetMax(max int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.max = max
	l.grantLocked()
}

// InUse is the number of slots held.
func (l *slotLimiter) InUse() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

// Waiting is the number of places in line.
func (l *slotLimiter) Waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiters)
}

// Max is the configured cap (zero or less: unlimited).
func (l *slotLimiter) Max() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max
}
