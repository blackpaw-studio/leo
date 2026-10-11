package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeAttach stands in for the tmux client in a PTY: the test writes what
// "tmux" prints into out, and observes resizes and the close.
type fakeAttach struct {
	r       *io.PipeReader
	out     *io.PipeWriter
	mu      sync.Mutex
	resizes [][2]uint16
	closed  chan struct{}
	once    sync.Once
}

func newFakeAttach() *fakeAttach {
	r, w := io.Pipe()
	return &fakeAttach{r: r, out: w, closed: make(chan struct{})}
}

func (f *fakeAttach) Read(p []byte) (int, error) { return f.r.Read(p) }

func (f *fakeAttach) Resize(cols, rows uint16) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]uint16{cols, rows})
	f.mu.Unlock()
	return nil
}

func (f *fakeAttach) Close() error {
	f.once.Do(func() {
		_ = f.r.CloseWithError(io.EOF)
		close(f.closed)
	})
	return nil
}

type attachHarness struct {
	srv     *Server
	ts      *httptest.Server
	proc    *fakeAttach
	ticks   chan time.Time
	mu      sync.Mutex
	session string
	size    [2]uint16
}

// newAttachHarness serves the real middleware chain on a real listener (a
// WebSocket needs a hijackable connection) with the PTY and ticker faked.
func newAttachHarness(t *testing.T) *attachHarness {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	s, _, _ := newDeviceTestServer(t, func(o *Options) { o.Port = port })
	h := &attachHarness{srv: s, ts: ts, proc: newFakeAttach(), ticks: make(chan time.Time)}
	s.startAttach = func(session string, cols, rows uint16) (attachProc, error) {
		h.mu.Lock()
		h.session, h.size = session, [2]uint16{cols, rows}
		h.mu.Unlock()
		return h.proc, nil
	}
	s.attachTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != attachHeartbeat {
			t.Errorf("heartbeat interval = %s, want %s", d, attachHeartbeat)
		}
		return h.ticks, func() {}
	}
	ts.Config.Handler = s.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return h
}

// dial opens the attach socket, returning the handshake status on failure.
// The connection is closed at test cleanup.
func (h *attachHarness) dial(t *testing.T, path, token string) (*websocket.Conn, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.ts.URL, "http")+path, &websocket.DialOptions{HTTPHeader: header})
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, status, err
}

func readFrame(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, data
}

func writeText(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(s)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func waitClosed(t *testing.T, f *fakeAttach) {
	t.Helper()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("attach process was not cleaned up")
	}
}

func TestAttachStreamsOutputAsBinaryFrames(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach?cols=120&rows=40", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	go func() { _, _ = h.proc.out.Write([]byte("\x1b[1mhello\x1b[0m")) }()
	typ, data := readFrame(t, c)
	if typ != websocket.MessageBinary || string(data) != "\x1b[1mhello\x1b[0m" {
		t.Fatalf("frame = %v %q", typ, data)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session != "leo-assistant" || h.size != [2]uint16{120, 40} {
		t.Fatalf("started session %q size %v", h.session, h.size)
	}
}

func TestAttachControlPingPongAndResize(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	writeText(t, c, `{"type":"ping"}`)
	typ, data := readFrame(t, c)
	if typ != websocket.MessageText || string(data) != `{"type":"pong"}` {
		t.Fatalf("ping reply = %v %q", typ, data)
	}

	writeText(t, c, `{"type":"resize","cols":100,"rows":30}`)
	writeText(t, c, `{"type":"ping"}`) // round-trip so the resize has been handled
	readFrame(t, c)
	h.proc.mu.Lock()
	resizes := slices.Clone(h.proc.resizes)
	h.proc.mu.Unlock()
	if !reflect.DeepEqual(resizes, [][2]uint16{{100, 30}}) {
		t.Fatalf("resizes = %v", resizes)
	}
}

func TestAttachSendsHeartbeatPing(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	h.ticks <- time.Now()
	typ, data := readFrame(t, c)
	if typ != websocket.MessageText || string(data) != `{"type":"ping"}` {
		t.Fatalf("heartbeat = %v %q", typ, data)
	}
}

func TestAttachRejectsInputFrames(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("rm -rf /\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("close status = %v (err %v), want policy violation", got, err)
	}
	waitClosed(t, h.proc)
}

func TestAttachCleansUpOnClientDisconnect(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.CloseNow()
	waitClosed(t, h.proc)
}

func TestAttachClosesWhenSessionEnds(t *testing.T) {
	h := newAttachHarness(t)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = h.proc.out.Close() // tmux client exited
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v (err %v), want normal", got, err)
	}
}

func TestAttachRefusals(t *testing.T) {
	h := newAttachHarness(t)
	cases := []struct {
		name, path, token string
		want              int
	}{
		{"unknown agent", "/api/v1/agents/ghost/attach", testAPIToken, http.StatusNotFound},
		{"agent token", "/api/v1/agents/assistant/attach", "agent-token", http.StatusForbidden},
		{"no token", "/api/v1/agents/assistant/attach", "", http.StatusUnauthorized},
		{"bad size", "/api/v1/agents/assistant/attach?cols=0", testAPIToken, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, status, err := h.dial(t, tc.path, tc.token)
			if err == nil {
				t.Fatal("dial succeeded")
			}
			if status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
		})
	}
}

func TestAttachStartFailureClosesWithError(t *testing.T) {
	h := newAttachHarness(t)
	h.srv.startAttach = func(string, uint16, uint16) (attachProc, error) {
		return nil, errors.New("no tmux")
	}
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", testAPIToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusInternalError {
		t.Fatalf("close status = %v (err %v), want internal error", got, err)
	}
}

func TestAttachCommandIsReadOnlyExactTarget(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	cmd := attachCommand("/usr/bin/tmux", "leo-assistant")
	// -u: tmux sanitizes non-ASCII output for a client it thinks lacks
	// UTF-8, and the launchd daemon has no LANG.
	want := []string{"/usr/bin/tmux", "-L", "leo", "-u", "attach-session", "-r", "-t", "=leo-assistant"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("argv = %q, want %q", cmd.Args, want)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "TMUX=") {
			t.Fatalf("TMUX leaked into attach env: %q", kv)
		}
	}
	if !slices.Contains(cmd.Env, "TERM=xterm-256color") {
		t.Fatalf("TERM missing: %q", cmd.Env)
	}
}
