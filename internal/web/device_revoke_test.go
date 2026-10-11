package web

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackpaw-studio/leo/internal/devices"
)

// revokeTicks replaces the revocation watcher's clock with a channel the
// test drives.
func revokeTicks(s *Server) chan time.Time {
	ticks := make(chan time.Time)
	s.revocationTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	return ticks
}

// tickUntil sends ticks until done closes, failing after a bound.
func tickUntil(t *testing.T, ticks chan<- time.Time, done <-chan struct{}) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-done:
			return
		case ticks <- time.Now():
		case <-deadline:
			t.Fatal("stream not closed after revoke")
		}
	}
}

// pairInStore pairs a device directly (these servers listen on a real port,
// so the Host the HTTP helpers send would not match).
func pairInStore(t *testing.T, store *devices.Store) pairResult {
	t.Helper()
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	return pairResult{DeviceID: dev.ID, Token: token}
}

func TestRevokeClosesOpenAttachSession(t *testing.T) {
	h := newAttachHarness(t)
	ticks := revokeTicks(h.srv)
	phone := pairInStore(t, h.srv.devices)
	c, _, err := h.dial(t, "/api/v1/agents/assistant/attach", phone.Token)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Not revoked yet: a check passes and the socket stays usable.
	ticks <- time.Now()
	writeText(t, c, `{"type":"ping"}`)
	if _, data := readFrame(t, c); string(data) != `{"type":"pong"}` {
		t.Fatalf("pre-revoke reply = %q", data)
	}

	if err := h.srv.devices.Revoke(phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		_, _, err := c.Read(context.Background())
		closed <- err
	}()
	done := make(chan struct{})
	var readErr error
	go func() { readErr = <-closed; close(done) }()
	tickUntil(t, ticks, done)
	if got := websocket.CloseStatus(readErr); got != websocket.StatusPolicyViolation {
		t.Fatalf("close status = %v (%v), want policy violation", got, readErr)
	}
	waitClosed(t, h.proc)
}

func TestRevokeClosesOpenEventStream(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	s, store, _ := newDeviceTestServer(t, func(o *Options) { o.Port = port })
	ticks := revokeTicks(s)
	ts.Config.Handler = s.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	phone := pairInStore(t, store)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+phone.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, "event: hello") {
		t.Fatalf("first line = %q", line)
	}

	if err := store.Revoke(phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			if _, err := br.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
	}()
	tickUntil(t, ticks, done)
}

func TestOperatorStreamsIgnoreRevocationWatch(t *testing.T) {
	s, _, _ := newDeviceTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	if revoked, stop := s.watchDeviceRevocation(req); revoked != nil {
		stop()
		t.Fatal("operator token got a revocation watch")
	}
}
