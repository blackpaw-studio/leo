package drain

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"
)

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeProber struct {
	mu     sync.Mutex
	probes map[string]Probe
	probed []string
}

func newFakeProber(probes ...Probe) *fakeProber {
	p := &fakeProber{probes: map[string]Probe{}}
	for _, probe := range probes {
		p.probes[probe.Name] = probe
	}
	return p
}

func (p *fakeProber) set(name string, busy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	probe := p.probes[name]
	probe.Busy = busy
	p.probes[name] = probe
}

func (p *fakeProber) remove(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.probes, name)
}

func (p *fakeProber) BusyAgents(context.Context) ([]Probe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Probe
	for _, name := range sortedKeys(p.probes) {
		if probe := p.probes[name]; probe.Busy {
			out = append(out, probe)
		}
	}
	return out, nil
}

func (p *fakeProber) Probe(_ context.Context, name string) (Probe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probed = append(p.probed, name)
	probe, ok := p.probes[name]
	if !ok {
		return Probe{Name: name}, nil
	}
	return probe, nil
}

func (p *fakeProber) probedNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.probed...)
}

type sentMessage struct{ name, text, id string }

type fakeNotifier struct {
	mu     sync.Mutex
	sent   []sentMessage
	queued map[string]bool  // agent -> deliveries are queued
	fail   map[string]error // agent -> next sends fail
	held   map[string]bool  // command id -> still in the outbox
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{queued: map[string]bool{}, fail: map[string]error{}, held: map[string]bool{}}
}

func (n *fakeNotifier) Message(_ context.Context, name, text, id string) (Delivery, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.fail[name]; err != nil {
		return Delivery{}, err
	}
	n.sent = append(n.sent, sentMessage{name, text, id})
	if n.queued[name] {
		n.held[id] = true
		return Delivery{Transport: TransportBridge, Queued: true}, nil
	}
	return Delivery{Transport: TransportBridge}, nil
}

func (n *fakeNotifier) Holds(_, id string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.held[id], nil
}

func (n *fakeNotifier) ack(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.held, id)
}

func (n *fakeNotifier) setFail(name string, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err == nil {
		delete(n.fail, name)
		return
	}
	n.fail[name] = err
}

func (n *fakeNotifier) messages() []sentMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]sentMessage(nil), n.sent...)
}

type fakeCron struct {
	mu       sync.Mutex
	held     bool
	released int
	tasks    []string
}

func (c *fakeCron) Hold() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = true
}

func (c *fakeCron) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = false
	c.released++
}

func (c *fakeCron) Held() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.tasks...)
}

type fakeStore struct {
	mu      sync.Mutex
	writes  []PendingResume
	removes int
}

func (s *fakeStore) Write(p PendingResume) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, p)
	return nil
}

func (s *fakeStore) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes++
	return nil
}
