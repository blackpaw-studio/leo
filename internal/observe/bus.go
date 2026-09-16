package observe

import (
	"sync"
	"time"
)

// busNow is injectable so tests get deterministic Event timestamps.
var busNow = time.Now

// subscriber pairs a channel with the lock that serializes send against
// close. Without this, a Publish that has already snapshotted the channel
// could race an in-flight unsubscribe and send on a closed channel.
type subscriber struct {
	mu     sync.Mutex
	ch     chan Event
	closed bool
}

// send delivers ev without blocking. It reports false if the channel is
// already closed or its buffer is full — either way the caller should drop
// this subscriber.
func (s *subscriber) send(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- ev:
		return true
	default:
		return false
	}
}

// close closes the channel exactly once, safe to call concurrently with send.
func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Bus fans out published events to every current subscriber. A subscriber
// that cannot keep up is dropped rather than allowed to stall a publisher —
// see Publish.
type Bus struct {
	mu     sync.Mutex
	seq    uint64
	nextID int
	subs   map[int]*subscriber

	// sendMu serializes the stamp+fan-out portion of Publish across
	// concurrent publishers, so delivery order always matches seq order —
	// see Publish's doc comment. It cannot be mu: drop (called from within
	// the send loop for a full subscriber) re-acquires mu, and a publisher
	// calling drop while itself holding mu would deadlock.
	sendMu sync.Mutex
}

// NewBus creates an empty event bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[int]*subscriber)}
}

// Publish stamps ev.Payload's Meta with the next sequence number and the
// current time, then delivers it to every subscriber. Delivery never blocks:
// a subscriber whose buffered channel is full (or already closed) is
// unsubscribed instead of stalling the publisher.
//
// sendMu is held for the whole seq-assignment+stamp+deliver body, so
// concurrent Publish calls run to completion one at a time, in the order
// they acquire sendMu — which is also, necessarily, seq order, since seq is
// assigned inside the same critical section. Without this (assigning seq
// under mu but stamping/sending unlocked), a publisher that got seq N could
// be preempted before stamping/sending, letting a publisher that got seq N+1
// deliver first — seq would jump backwards for subscribers, defeating its
// purpose as a gap-detection signal.
func (b *Bus) Publish(ev Event) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	b.mu.Lock()
	b.seq++
	seq := b.seq
	at := busNow()
	ids := make([]int, 0, len(b.subs))
	subs := make([]*subscriber, 0, len(b.subs))
	for id, s := range b.subs {
		ids = append(ids, id)
		subs = append(subs, s)
	}
	b.mu.Unlock()

	if publishTestHook != nil {
		publishTestHook(seq)
	}

	ev.Payload.stamp(seq, at)

	for i, s := range subs {
		if !s.send(ev) {
			b.drop(ids[i])
		}
	}
}

// publishTestHook, when non-nil, is invoked with the just-assigned seq right
// after mu is released but while sendMu is still held. It exists solely so
// tests can deterministically force the delivery-ordering race window this
// method closes; production code never sets it.
var publishTestHook func(seq uint64)

// Subscribe registers a new subscriber with the given channel buffer size and
// returns the receive-only channel, an unsubscribe function, and the
// sequence number of the last event published before this subscriber was
// registered (0 if none have been published yet). The unsubscribe function
// closes the channel and is safe to call more than once.
//
// Registration and reading that starting sequence happen under one
// acquisition of mu, so no Publish can land between them: every event
// assigned a seq after this call is guaranteed to reach this subscriber, and
// no event counted in the returned seq was ever missed. That makes the
// returned seq safe to use verbatim as a "hello" frame's starting point — the
// first event this subscriber actually receives is guaranteed to be
// seq+1, with no window in which an event is both counted and never
// delivered. See the eventSource interface in internal/web.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func(), uint64) {
	if buffer < 0 {
		buffer = 0
	}
	s := &subscriber{ch: make(chan Event, buffer)}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = s
	seq := b.seq
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() { b.drop(id) })
	}
	return s.ch, unsub, seq
}

// drop removes and closes the subscriber if it is still registered. Safe to
// race with an in-flight unsubscribe or another drop for the same id — only
// the caller that actually removes the map entry closes the channel.
func (b *Bus) drop(id int) {
	b.mu.Lock()
	s, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
	}
	b.mu.Unlock()
	if ok {
		s.close()
	}
}
