package hosts

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestFanInTagsHostWithoutEcho(t *testing.T) {
	sock := shortEventSock(t, "remote.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "scope=local" {
			t.Errorf("query = %q, want scope=local", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: hello\ndata: {}\n\nevent: host_state_changed\ndata: {\"host\":\"localhost\"}\n\nevent: agent_spawned\ndata: {\"host\":\"localhost\",\"agent\":\"a\"}\n\nevent: agent_spawned\ndata: {\"host\":\"other\",\"agent\":\"b\"}\n\n")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	h := testHub(t)
	h.bus = observe.NewBus()
	c := h.conns["x"]
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	ch, stop, _ := h.bus.Subscribe(4)
	defer stop()
	if err := c.fanInOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		p := ev.Payload.(*observe.RawPayload).Fields
		if ev.Type != observe.EventAgentSpawned || p["host"] != "x" || p["agent"] != "a" {
			t.Fatalf("republished event = %s %#v", ev.Type, p)
		}
	default:
		t.Fatal("missing republished event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected republished event: %s %#v", ev.Type, ev.Payload)
	case <-time.After(10 * time.Millisecond):
	}
}

func TestReconnectRestartsSubscription(t *testing.T) {
	m := newMachine(t)
	m.hub.bus = observe.NewBus()
	c := m.hub.conns["x"]
	sock := shortEventSock(t, "reconnect.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	firstCancelled := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if n == 1 {
			<-r.Context().Done()
			close(firstCancelled)
			return
		}
		if n == 2 {
			_, _ = fmt.Fprint(w, "event: agent_spawned\ndata: {\"host\":\"localhost\",\"agent\":\"second\"}\n\n")
			w.(http.Flusher).Flush()
			close(secondStarted)
			<-releaseSecond
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(releaseSecond); _ = srv.Close() })
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	events, stop, _ := m.hub.bus.Subscribe(16)
	defer stop()
	_ = c.connect(context.Background(), true)
	p1 := <-m.starts
	tick1 := <-m.clock.ticks
	m.health <- true
	tick1.ch <- m.clock.Now()
	spin(t, func() bool { return requests.Load() == 1 })
	<-m.clock.ticks
	p1.done <- fmt.Errorf("forward exited")
	spin(t, func() bool {
		select {
		case <-firstCancelled:
			return true
		default:
			return false
		}
	})
	m.clock.fireAllAfter(t, time.Second)
	<-m.starts
	tick2 := <-m.clock.ticks
	m.health <- true
	tick2.ch <- m.clock.Now()
	spin(t, func() bool {
		select {
		case <-secondStarted:
			return true
		default:
			return false
		}
	})
	var tagged bool
	spin(t, func() bool {
		select {
		case ev := <-events:
			if ev.Type == observe.EventAgentSpawned {
				p := ev.Payload.(*observe.RawPayload).Fields
				tagged = p["host"] == "x" && p["agent"] == "second"
			}
			return tagged
		default:
			return false
		}
	})
	if got := requests.Load(); got != 2 {
		t.Fatalf("SSE subscriptions = %d, want exactly 2", got)
	}
}

func TestCleanSSEEOFResubscribesWithinAttempt(t *testing.T) {
	h := testHub(t)
	h.bus = observe.NewBus()
	clock := newFakeClock()
	h.deps.clock = clock
	c := h.conns["x"]
	sock := shortEventSock(t, "eof.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "scope=local" {
			t.Errorf("query=%q", r.URL.RawQuery)
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = fmt.Fprint(w, "event: agent_spawned\ndata: {\"host\":\"localhost\",\"agent\":\"one\"}\n\nevent: agent_spawned\ndata: {\"host\":\"localhost\",\"agent\":\"two\"}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "event: agent_spawned\ndata: {\"host\":\"localhost\",\"agent\":\"three\"}\n\n")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	events, stop, _ := h.bus.Subscribe(8)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.fanInLoop(ctx) }()
	for i := 0; i < 2; i++ {
		<-events
	}
	clock.fireAfter(t, time.Second)
	spin(t, func() bool { return requests.Load() >= 2 })
	ev := <-events
	p := ev.Payload.(*observe.RawPayload).Fields
	if p["agent"] != "three" || p["host"] != "x" {
		t.Fatalf("event after clean EOF = %#v, want tagged third event", p)
	}
	cancel()
	<-done
}

func shortEventSock(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "leo-events-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}
