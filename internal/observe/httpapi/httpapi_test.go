package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTicker struct{ ch chan time.Time }

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               {}

type fakeClock struct {
	now      time.Time
	ticker   *fakeTicker
	duration time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) NewTicker(d time.Duration) Ticker {
	c.duration = d
	return c.ticker
}

type notifyingWriter struct {
	header http.Header
	bytes.Buffer
	flushed chan struct{}
}

func (w *notifyingWriter) Header() http.Header { return w.header }
func (w *notifyingWriter) WriteHeader(int)     {}
func (w *notifyingWriter) Flush() {
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func TestServeEventsHelloFirstAndDefaultPingCadence(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0), ticker: &fakeTicker{ch: make(chan time.Time)}}
	w := &notifyingWriter{header: make(http.Header), flushed: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ServeEvents(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx), EventsOptions{Clock: clock})
		close(done)
	}()
	<-w.flushed // hello
	clock.ticker.ch <- clock.now
	<-w.flushed // ping
	cancel()
	<-done
	if clock.duration != 20*time.Second {
		t.Fatalf("ticker duration = %s, want 20s", clock.duration)
	}
	if got := w.String(); !strings.HasPrefix(got, "event: hello\n") || !strings.Contains(got, "\n\n: ping\n\n") {
		t.Fatalf("stream = %q, want hello first then ping", got)
	}
}

func TestWriteEventUsesNamedJSONFrame(t *testing.T) {
	var b bytes.Buffer
	if err := WriteEvent(&b, "agent_stopped", map[string]string{"agent": "a"}); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "event: agent_stopped\ndata: {\"agent\":\"a\"}\n\n"; got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}
}

func TestServeEventsHelloCarriesBootID(t *testing.T) {
	for _, tc := range []struct {
		name, opt, want string
	}{
		{"explicit", "boot-123", "boot-123"},
		{"process default", "", BootID()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(100, 0), ticker: &fakeTicker{ch: make(chan time.Time)}}
			w := &notifyingWriter{header: make(http.Header), flushed: make(chan struct{}, 2)}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				ServeEvents(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx), EventsOptions{Clock: clock, BootID: tc.opt})
				close(done)
			}()
			<-w.flushed
			cancel()
			<-done
			if tc.want == "" || !strings.Contains(w.String(), `"boot_id":"`+tc.want+`"`) {
				t.Fatalf("hello = %q, want boot_id %q", w.String(), tc.want)
			}
		})
	}
}

func TestBootIDIsStableWithinProcess(t *testing.T) {
	if BootID() == "" || BootID() != BootID() {
		t.Fatalf("BootID() = %q, want stable non-empty", BootID())
	}
}
