package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/observe/httpapi"
)

type streamingRecorder struct {
	header http.Header
	bytes.Buffer
	flushed chan struct{}
}

func newStreamingRecorder() *streamingRecorder {
	return &streamingRecorder{header: make(http.Header), flushed: make(chan struct{}, 4)}
}
func (w *streamingRecorder) Header() http.Header { return w.header }
func (w *streamingRecorder) WriteHeader(int)     {}
func (w *streamingRecorder) Flush()              { w.flushed <- struct{}{} }

type daemonFakeTicker struct{ ch chan time.Time }

func (t *daemonFakeTicker) C() <-chan time.Time { return t.ch }
func (t *daemonFakeTicker) Stop()               {}

type daemonFakeClock struct {
	now    time.Time
	ticker *daemonFakeTicker
}

func (c *daemonFakeClock) Now() time.Time { return c.now }
func (c *daemonFakeClock) NewTicker(time.Duration) httpapi.Ticker {
	return c.ticker
}

func TestEventsWithoutObservabilityReturnsUnavailable(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "leo.sock"), filepath.Join(t.TempDir(), "leo.yaml"), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events", nil))
	if got, want := w.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want %d; body=%s", got, want, w.Body.String())
	}
	if got, want := w.Body.String(), "{\"ok\":false,\"error\":\"observability unavailable\",\"code\":\"observability_unavailable\"}\n"; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestEventsRouteStreamsBusEventAndPing(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "leo.sock"), filepath.Join(dir, "leo.yaml"), nil)
	bus := observe.NewBus()
	s.SetObservability(bus, nil, nil, nil, "test")
	clock := &daemonFakeClock{now: time.Unix(100, 0), ticker: &daemonFakeTicker{ch: make(chan time.Time)}}
	s.observeClock = clock

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w := newStreamingRecorder()
	go func() {
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx))
		close(done)
	}()
	<-w.flushed // hello
	bus.Publish(observe.Event{Type: observe.EventAgentStopped, Payload: &observe.AgentStoppedPayload{Agent: "local"}})
	<-w.flushed // event
	clock.ticker.ch <- clock.now
	<-w.flushed // ping
	cancel()
	<-done

	got := w.String()
	if !strings.HasPrefix(got, "event: hello\n") || !strings.Contains(got, "event: agent_stopped\n") || !strings.Contains(got, "\n\n: ping\n\n") {
		t.Fatalf("stream = %q", got)
	}
}

func TestLocalMetadataRoutesUseExactEnvelopeShapes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{
		"coding": {Model: "opus", Workspace: "/work", HarnessOptions: map[string]any{"agent": "reviewer"}},
	}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "leo.sock"), cfgPath, nil)
	s.SetObservability(nil, nil, nil, nil, "v-test")

	for _, path := range []string{"/health", "/version"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got, want := w.Body.String(), "{\"ok\":true,\"data\":{\"version\":\"v-test\"}}\n"; got != want {
			t.Fatalf("%s = %s, want %s", path, got, want)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/templates", nil))
	if got, want := w.Body.String(), "{\"ok\":true,\"data\":[{\"name\":\"coding\",\"model\":\"opus\",\"agent\":\"reviewer\",\"workspace\":\"/work\"}]}\n"; got != want {
		t.Fatalf("/templates = %s, want %s", got, want)
	}
}

func TestLocalStateContainsWebAgentRowsWithoutHost(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := config.Save(cfgPath, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "leo.sock"), cfgPath, nil)
	s.SetAgentManager(&fakeAgentManager{records: []agent.Record{{Name: "local", Status: "running"}}})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)
	row := data["agents"].([]any)[0].(map[string]any)
	if body["ok"] != true || row["name"] != "local" {
		t.Fatalf("unexpected state: %s", w.Body.String())
	}
	if _, exists := row["host"]; exists {
		t.Fatalf("local state contains host: %s", w.Body.String())
	}
}
