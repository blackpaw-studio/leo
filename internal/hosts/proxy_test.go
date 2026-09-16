package hosts

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestLocalhostProxyDispatch(t *testing.T) {
	h := New(&config.Config{}, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	w := httptest.NewRecorder()
	if err := h.Proxy(w, httptest.NewRequest("GET", "/hosts/localhost/agents/list", nil), "localhost"); err != nil || w.Code != 201 {
		t.Fatal(err, w.Code)
	}
}

func TestUnknownHost404(t *testing.T) {
	h := New(&config.Config{}, nil, nil)
	err := h.Proxy(httptest.NewRecorder(), httptest.NewRequest("GET", "/hosts/no/agents/list", nil), "no")
	if err == nil {
		t.Fatal("expected")
	}
}

func TestDisconnectedProxy503(t *testing.T) {
	h := testHub(t)
	h.connectTimeout = time.Millisecond
	h.conns["x"].transition(StateConnecting, nil)
	if err := h.Proxy(httptest.NewRecorder(), httptest.NewRequest("GET", "/hosts/x/agents/list", nil), "x"); err == nil {
		t.Fatal("expected")
	}
}

func TestProxyPassthrough(t *testing.T) {
	sock := t.TempDir() + "/remote.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		w.WriteHeader(418)
		_, _ = io.WriteString(w, `{"ok":false,"error":"teapot","code":"x"}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	m := newMachine(t)
	h := m.hub
	c := h.conns["x"]
	_ = c.connect(context.Background(), true)
	<-m.starts
	tick := <-m.clock.ticks
	m.health <- true
	tick.ch <- m.clock.Now()
	spin(t, func() bool { return c.stateValue() == StateConnected })
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	w := httptest.NewRecorder()
	if err := h.Proxy(w, httptest.NewRequest("GET", "/hosts/x/agents/list", nil), "x"); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "/agents/list" {
		t.Fatalf("remote path = %q, want /agents/list", got)
	}
	if w.Code != 418 || w.Body.String() != `{"ok":false,"error":"teapot","code":"x"}` {
		t.Fatalf("passthrough = %d %q", w.Code, w.Body.String())
	}
	_ = srv.Close()
	c.transport.CloseIdleConnections()
	w = httptest.NewRecorder()
	_ = h.Proxy(w, httptest.NewRequest("GET", "/hosts/x/agents/list", nil), "x")
	if w.Code != 503 || !containsJSON(w.Body.Bytes(), `"ok":false`, `"code":"host_unavailable"`) {
		t.Fatalf("proxy failure = %d %s", w.Code, w.Body.String())
	}
	spin(t, func() bool { return c.stateValue() == StateDisconnected })
}

func containsJSON(b []byte, parts ...string) bool {
	s := string(b)
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
