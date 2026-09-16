package hosts

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type fakeTicker struct{ ch chan time.Time }

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (*fakeTicker) Stop()                 {}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	afters map[time.Duration][]chan time.Time
	ticks  chan *fakeTicker
}

func TestStaleConnectedAfterDisconnectIgnored(t *testing.T) {
	m := newMachine(t)
	c := m.hub.conns["x"]
	if err := c.connect(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	old := c.attemptValue()
	<-m.starts
	if err := c.disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.updates <- connectionUpdate{attempt: old, state: StateConnected}
	spin(t, func() bool { return len(c.updates) == 0 })
	if got := c.stateValue(); got != StateDisconnected {
		t.Fatalf("state=%s", got)
	}
}

func TestStaleDownAfterReconnectIgnored(t *testing.T) {
	m := newMachine(t)
	c := m.hub.conns["x"]
	_ = c.connect(context.Background(), true)
	old := c.attemptValue()
	<-m.starts
	_ = c.disconnect(context.Background())
	_ = c.connect(context.Background(), true)
	<-m.starts
	_ = c.send(context.Background(), connectionCommand{kind: "down", attempt: old, ack: make(chan error, 1)})
	if got := c.stateValue(); got != StateConnecting {
		t.Fatalf("state=%s", got)
	}
}

func TestDownSchedulesReconnect(t *testing.T) {
	m := newMachine(t)
	c := m.hub.conns["x"]
	_ = c.connect(context.Background(), true)
	id := c.attemptValue()
	<-m.starts
	c.markDown(id)
	m.clock.fireAfter(t, time.Second)
	spin(t, func() bool { return len(m.starts) == 1 })
}

func TestQueuedConnectedAfterDownIgnored(t *testing.T) {
	m := newMachine(t)
	c := m.hub.conns["x"]
	_ = c.connect(context.Background(), true)
	id := c.attemptValue()
	<-m.starts
	c.markDown(id)
	c.updates <- connectionUpdate{attempt: id, state: StateConnected}
	spin(t, func() bool { return len(c.updates) == 0 })
	if got := c.stateValue(); got != StateDisconnected {
		t.Fatalf("queued Connected resurrected state: %s", got)
	}
}

func TestRemediationIncludesSSHArgsInOrder(t *testing.T) {
	he := remediate(config.HostConfig{SSH: "u@host", SSHArgs: []string{"-p", "2222", "-i", "key"}}, &HostError{Code: "ssh_auth_required", Message: "denied"})
	if !strings.Contains(he.Message, "run: ssh -p 2222 -i key u@host") {
		t.Fatal(he.Message)
	}
}

func TestExplicitConnectRestartsAfterTerminalExit(t *testing.T) {
	m := newMachine(t)
	var calls int
	m.hub.deps.discover = func(context.Context, string, []string, string) (string, error) {
		calls++
		if calls == 1 {
			return "", &HostError{Code: "ssh_auth_required", Message: "denied"}
		}
		return "/remote.sock", nil
	}
	c := m.hub.conns["x"]
	_ = c.connect(context.Background(), true)
	spin(t, func() bool { return c.stateValue() == StateError })
	_ = c.connect(context.Background(), true)
	spin(t, func() bool { return len(m.starts) == 1 })
	<-m.starts
	if calls != 2 {
		t.Fatalf("discover calls=%d", calls)
	}
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(100, 0), afters: map[time.Duration][]chan time.Time{}, ticks: make(chan *fakeTicker, 8)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.afters[d] = append(c.afters[d], ch)
	c.mu.Unlock()
	return ch
}
func (c *fakeClock) NewTicker(time.Duration) Ticker {
	t := &fakeTicker{ch: make(chan time.Time, 8)}
	c.ticks <- t
	return t
}
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *fakeClock) fireAfter(t *testing.T, d time.Duration) {
	t.Helper()
	var ch chan time.Time
	spin(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		xs := c.afters[d]
		if len(xs) == 0 {
			return false
		}
		ch = xs[0]
		c.afters[d] = xs[1:]
		return true
	})
	ch <- c.Now()
}
func (c *fakeClock) fireAllAfter(t *testing.T, d time.Duration) {
	t.Helper()
	spin(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.afters[d]) > 0 })
	c.mu.Lock()
	xs := c.afters[d]
	c.afters[d] = nil
	c.mu.Unlock()
	for _, ch := range xs {
		ch <- c.Now()
	}
}

type fakeProcess struct {
	done   chan error
	stderr string
	killed bool
}

func newFakeProcess() *fakeProcess        { return &fakeProcess{done: make(chan error, 2)} }
func (p *fakeProcess) Done() <-chan error { return p.done }
func (p *fakeProcess) Kill()              { p.killed = true; p.done <- context.Canceled }
func (p *fakeProcess) Stderr() string     { return p.stderr }
func spin(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 1000000; i++ {
		if ok() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("condition not reached")
}

type machine struct {
	hub    *Hub
	clock  *fakeClock
	starts chan *fakeProcess
	health chan bool
	opsMu  sync.Mutex
	ops    []string
}

func testHub(t *testing.T) *Hub {
	t.Helper()
	return New(&config.Config{HomePath: t.TempDir(), Client: config.ClientConfig{Hosts: map[string]config.HostConfig{"x": {SSH: "x"}}}}, nil, nil)
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	clock := newFakeClock()
	cfg := &config.Config{HomePath: t.TempDir(), Client: config.ClientConfig{Hosts: map[string]config.HostConfig{"x": {SSH: "u@x"}}}}
	h := New(cfg, nil, nil)
	m := &machine{hub: h, clock: clock, starts: make(chan *fakeProcess, 8), health: make(chan bool, 8)}
	h.deps = dependencies{clock: clock, discover: func(context.Context, string, []string, string) (string, error) { return "/remote.sock", nil }, start: func(context.Context, config.HostConfig, string, string, string) (forwardProcess, error) {
		p := newFakeProcess()
		m.starts <- p
		return p, nil
	}, health: func(string) bool { return <-m.health }, exit: func(context.Context, config.HostConfig, string) { m.record("exit") }, remove: func(...string) { m.record("remove") }}
	t.Cleanup(func() { _ = h.Close() })
	return m
}
func (m *machine) record(s string) { m.opsMu.Lock(); defer m.opsMu.Unlock(); m.ops = append(m.ops, s) }
func nextEvent(t *testing.T, ch <-chan observe.Event) map[string]any {
	t.Helper()
	var ev observe.Event
	spin(t, func() bool {
		select {
		case ev = <-ch:
			return true
		default:
			return false
		}
	})
	return ev.Payload.(*observe.RawPayload).Fields
}

func TestConnectionTransitions(t *testing.T) {
	m := newMachine(t)
	m.hub.bus = observe.NewBus()
	sock := t.TempDir() + "/events.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	events, stop, _ := m.hub.bus.Subscribe(16)
	defer stop()
	c := m.hub.conns["x"]
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.connect(context.Background(), true) }()
	}
	wg.Wait()
	p := <-m.starts
	if len(m.starts) != 0 {
		t.Fatal("concurrent connect started more than one worker")
	}
	if got := nextEvent(t, events)["state"]; got != StateConnecting {
		t.Fatal(got)
	}
	tick := <-m.clock.ticks
	m.health <- true
	tick.ch <- m.clock.Now()
	if got := nextEvent(t, events)["state"]; got != StateConnected {
		t.Fatal(got)
	}
	p.done <- errors.New("drop")
	if got := nextEvent(t, events)["state"]; got != StateDisconnected {
		t.Fatal(got)
	}
	m.clock.fireAllAfter(t, time.Second)
	spin(t, func() bool { return len(m.starts) > 0 })
	<-m.starts
	if got := nextEvent(t, events)["state"]; got != StateConnecting {
		t.Fatal(got)
	}
}
func TestBackoffResetsAfterHealthyMinute(t *testing.T) {
	m := newMachine(t)
	c := m.hub.conns["x"]
	c.start(context.Background())
	p1 := <-m.starts
	p1.done <- errors.New("early")
	m.clock.fireAllAfter(t, time.Second)
	p2 := <-m.starts
	<-m.clock.ticks
	tick := <-m.clock.ticks
	m.health <- true
	tick.ch <- m.clock.Now()
	spin(t, func() bool { return c.stateValue() == StateConnected })
	m.clock.advance(61 * time.Second)
	p2.done <- errors.New("drop")
	m.clock.fireAllAfter(t, time.Second)
}
func TestLazyConnectTimeout(t *testing.T) {
	m := newMachine(t)
	m.hub.connectTimeout = 19 * time.Second
	c := m.hub.conns["x"]
	c.transition(StateConnecting, nil)
	done := make(chan error, 1)
	go func() { _, err := m.hub.ensure(context.Background(), "x"); done <- err }()
	m.clock.fireAfter(t, 19*time.Second)
	if err := <-done; err == nil {
		t.Fatal("expected timeout")
	}
}
func TestCloseStopsThenRemovesSockets(t *testing.T) {
	m := newMachine(t)
	m.hub.bus = observe.NewBus()
	events, stop, _ := m.hub.bus.Subscribe(4)
	defer stop()
	c := m.hub.conns["x"]
	c.transition(StateConnected, nil)
	_ = nextEvent(t, events)
	if err := m.hub.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.ops, []string{"exit", "remove"}) {
		t.Fatalf("ops=%v", m.ops)
	}
	if got := nextEvent(t, events)["state"]; got != StateDisconnected {
		t.Fatal(got)
	}
}
