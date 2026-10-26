package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/clienttools"
	"github.com/blackpaw-studio/leo/internal/leotools"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func newClientToolsDaemon(t *testing.T) (*Server, *clienttools.Relay, *observe.Bus) {
	t.Helper()
	bus := observe.NewBus()
	relay := clienttools.NewRelay(clienttools.RelayOptions{
		Registry: clienttools.RegistryOptions{IsBuiltin: leotools.IsKnownTool},
		Flag:     clienttools.NewEverRegistered(clienttools.NewFileFlagStore(filepath.Join(t.TempDir(), "client-tools.json")), nil),
		Publish:  bus,
	})
	dir := t.TempDir()
	s := New(filepath.Join(dir, "leo.sock"), filepath.Join(dir, "leo.yaml"), nil, WithClientTools(relay))
	s.SetObservability(bus, nil, nil, nil, "test")
	return s, relay, bus
}

func ctSocketDo(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestSocketServesClientToolsPaths(t *testing.T) {
	s, relay, _ := newClientToolsDaemon(t)
	id := string(relay.Connect())

	w := ctSocketDo(s, http.MethodPost, "/api/v1/client-tools", `{"client_id":"`+id+`","group":"browser","tools":[{"name":"browser_click","description":"d","input_schema":{"type":"object"},"timeout_s":30}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	w = ctSocketDo(s, http.MethodGet, "/api/v1/client-tools", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"browser_click"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/v1/client-tools/requests?pending=1"} {
		if w := ctSocketDo(s, http.MethodGet, path, ""); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := ctSocketDo(s, http.MethodPost, "/api/v1/client-tools/requests/ctr_nope/claim", `{"client_id":"`+id+`"}`); w.Code != http.StatusNotFound {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
}

func TestSocketClientToolsUnavailableWithoutRelay(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "leo.sock"), filepath.Join(dir, "leo.yaml"), nil)
	if w := ctSocketDo(s, http.MethodGet, "/api/v1/client-tools", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}

func TestSocketProviderStreamGetsClientID(t *testing.T) {
	s, relay, _ := newClientToolsDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := newStreamingRecorder()
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events?client_tools=1", nil).WithContext(ctx))
		close(done)
	}()
	<-w.flushed // hello
	hello := w.String()
	i := strings.Index(hello, `"client_id":"`)
	if i < 0 {
		t.Fatalf("hello = %q", hello)
	}
	id := hello[i+len(`"client_id":"`):]
	end := strings.Index(id, `"`)
	if end < 0 {
		t.Fatalf("unterminated client_id in %q", hello)
	}
	id = id[:end]
	if !relay.ClientExists(clienttools.ClientID(id)) {
		t.Fatalf("client %q not connected", id)
	}
	cancel()
	<-done
	if relay.ClientExists(clienttools.ClientID(id)) {
		t.Fatal("client survives its stream")
	}
}

func TestGoneHookCancelsParkedClientToolCall(t *testing.T) {
	s, relay, bus := newClientToolsDaemon(t)
	events, unsub, _ := bus.Subscribe(16)
	defer unsub()
	id := relay.Connect()
	if _, err := relay.Register(id, "browser", []clienttools.Tool{{Name: "browser_handoff", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
		t.Fatal(err)
	}

	got := make(chan clienttools.Result, 1)
	go func() {
		res, _ := relay.Call(context.Background(), "alfred", "browser_handoff", nil)
		got <- res
	}()
	waitFor(t, events, observe.EventClientToolRequest)

	s.AgentGone("alfred")

	select {
	case res := <-got:
		if res.OK || res.Error == nil || res.Error.Code != clienttools.CodeCancelled {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked call survived its agent")
	}
}

func waitFor(t *testing.T, events <-chan observe.Event, typ observe.EventType) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == typ {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}
