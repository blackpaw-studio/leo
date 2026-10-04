package daemon

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

const bridgeAgent = "leo-alpha"

// startBridgeServer starts a daemon whose bridge hub is the one returned, so
// a test can drive the outbox directly and observe it over the socket.
func startBridgeServer(t *testing.T, configure ...func(*Server)) (string, *bridge.Hub, *Server) {
	t.Helper()
	workDir := tmpWorkDir(t)
	hub := bridge.New(bridge.Options{})
	s := New(SockPath(workDir), "/tmp/leo.yaml", nil, WithBridge(hub))
	for _, fn := range configure {
		fn(s)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return workDir, hub, s
}

func bridgeTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// openStream opens agent's stream through the production client and waits
// until the hub has registered it.
func openStream(t *testing.T, workDir string, hub *bridge.Hub, agent string) *bufio.Reader {
	t.Helper()
	ctx := bridgeTestCtx(t)
	body, err := OpenBridgeStream(ctx, workDir, agent)
	if err != nil {
		t.Fatalf("OpenBridgeStream: %v", err)
	}
	t.Cleanup(func() { _ = body.Close() })
	if _, err := hub.WaitFor(ctx, agent, func(s bridge.State) bool { return s.Connected }); err != nil {
		t.Fatalf("stream never registered: %v", err)
	}
	return bufio.NewReader(body)
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("reading stream line: %v (partial %q)", err, line)
	}
	return line
}

func TestBridgeStreamWritesQueuedCommandsAsNDJSON(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	first, err := hub.Enqueue(bridgeAgent, bridge.Deliver("hello\nworld", true))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	second, _ := hub.Enqueue(bridgeAgent, bridge.Interrupt())

	r := openStream(t, workDir, hub, bridgeAgent)
	if got, want := readLine(t, r), `{"id":"`+first+`","op":"deliver","text":"hello\nworld","as_user":true}`+"\n"; got != want {
		t.Fatalf("line 1\n got: %q\nwant: %q", got, want)
	}
	if got, want := readLine(t, r), `{"id":"`+second+`","op":"interrupt"}`+"\n"; got != want {
		t.Fatalf("line 2\n got: %q\nwant: %q", got, want)
	}
}

// Each command must reach the client as soon as it is queued; a missing
// flush would leave it sitting in the server's buffer and hang this read.
func TestBridgeStreamFlushesEachCommandImmediately(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	r := openStream(t, workDir, hub, bridgeAgent)
	id, _ := hub.Enqueue(bridgeAgent, bridge.Clear())
	if got := readLine(t, r); !strings.Contains(got, id) {
		t.Fatalf("got %q, want command %s", got, id)
	}
}

func TestBridgeStreamContentType(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodGet, "http://daemon/api/bridge/"+bridgeAgent+"/stream", nil)
	resp, err := newUnixClientNoTimeout(SockPath(workDir)).Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status=%d content-type=%q, want 200 application/x-ndjson", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestBridgeStreamEndsWhenReplaced(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	old := openStream(t, workDir, hub, bridgeAgent)
	ctx := bridgeTestCtx(t)
	newer, err := OpenBridgeStream(ctx, workDir, bridgeAgent)
	if err != nil {
		t.Fatalf("second OpenBridgeStream: %v", err)
	}
	defer newer.Close()

	if _, err := old.ReadString('\n'); !errors.Is(err, io.EOF) {
		t.Fatalf("replaced stream read err=%v, want a clean EOF", err)
	}
	// The replaced handler's cleanup must leave the new stream registered.
	id, _ := hub.Enqueue(bridgeAgent, bridge.Clear())
	if got := readLine(t, bufio.NewReader(newer)); !strings.Contains(got, id) {
		t.Fatalf("new stream got %q, want %s", got, id)
	}
	if !hub.Connected(bridgeAgent) {
		t.Fatal("agent not connected after the replaced stream ended")
	}
}

func TestBridgeStreamUnregistersOnClientDisconnect(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	ctx, cancel := context.WithCancel(bridgeTestCtx(t))
	body, err := OpenBridgeStream(ctx, workDir, bridgeAgent)
	if err != nil {
		t.Fatalf("OpenBridgeStream: %v", err)
	}
	if _, err := hub.WaitFor(bridgeTestCtx(t), bridgeAgent, func(s bridge.State) bool { return s.Connected }); err != nil {
		t.Fatalf("stream never registered: %v", err)
	}
	cancel()
	_ = body.Close()
	if _, err := hub.WaitFor(bridgeTestCtx(t), bridgeAgent, func(s bridge.State) bool { return !s.Connected }); err != nil {
		t.Fatalf("disconnected stream stayed registered: %v", err)
	}
}

// The socket server's 30s WriteTimeout would otherwise fail the first write
// to a stream that idled past it. Shrunk here so the test can outlast it;
// ReadTimeout is shrunk too to pin that it does not end the stream either.
func TestBridgeStreamOutlivesServerTimeouts(t *testing.T) {
	const serverTimeout = 50 * time.Millisecond
	workDir, hub, _ := startBridgeServer(t, func(s *Server) {
		s.httpServer.ReadTimeout = serverTimeout
		s.httpServer.WriteTimeout = serverTimeout
	})
	r := openStream(t, workDir, hub, bridgeAgent)
	// Not synchronization: real elapsed time is the thing under test.
	time.Sleep(6 * serverTimeout)
	if !hub.Connected(bridgeAgent) {
		t.Fatal("idle stream was dropped by the server timeouts")
	}
	id, _ := hub.Enqueue(bridgeAgent, bridge.Clear())
	if got := readLine(t, r); !strings.Contains(got, id) {
		t.Fatalf("got %q, want %s", got, id)
	}
}

func TestBridgeStreamRejectsInvalidAgent(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	for _, agent := range []string{"bad%20name", "a%2Fb", "x%3Fy"} {
		req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodGet, "http://daemon/api/bridge/"+agent+"/stream", nil)
		resp, err := newUnixClientNoTimeout(SockPath(workDir)).Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", agent, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("agent %q: status=%d, want 400", agent, resp.StatusCode)
		}
	}
}

func TestBridgeShutdownEndsStreams(t *testing.T) {
	workDir, hub, s := startBridgeServer(t)
	r := openStream(t, workDir, hub, bridgeAgent)
	start := time.Now()
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Without closing the hub, Shutdown waits out its 5s grace on the
	// still-active stream handler.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Shutdown took %s; streams were not ended", elapsed)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("stream still open after Shutdown")
	}
}

func TestBridgeReportAckSettlesSend(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	r := openStream(t, workDir, hub, bridgeAgent)
	done := make(chan error, 1)
	go func() { done <- hub.Send(bridgeTestCtx(t), bridgeAgent, bridge.Deliver("hi", false)) }()

	line := readLine(t, r)
	id := line[strings.Index(line, `"id":"`)+6:]
	id = id[:strings.Index(id, `"`)]
	if err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, []byte(`{"type":"ack","id":"`+id+`","ok":true}`)); err != nil {
		t.Fatalf("PostBridgeReport: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never observed the ack")
	}
}

func TestBridgeReportEventUpdatesState(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	if err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, []byte(`{"type":"event","name":"turn.start"}`)); err != nil {
		t.Fatalf("PostBridgeReport: %v", err)
	}
	if !hub.State(bridgeAgent).Busy {
		t.Fatal("turn.start report did not mark the agent busy")
	}
}

func TestBridgeReportRejectsBadRequests(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	cases := []struct {
		name   string
		agent  string
		body   string
		status int
	}{
		{"unknown type", bridgeAgent, `{"type":"bogus"}`, http.StatusBadRequest},
		{"not json", bridgeAgent, `nope`, http.StatusBadRequest},
		{"unknown field", bridgeAgent, `{"type":"event","name":"turn.start","x":1}`, http.StatusBadRequest},
		{"invalid agent", "bad%20name", `{"type":"event","name":"turn.start"}`, http.StatusBadRequest},
		{"oversized", bridgeAgent, `{"type":"event","name":"turn.complete","usage":{"pad":"` + strings.Repeat("x", 64<<10) + `"}}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodPost,
				"http://daemon/api/bridge/"+tc.agent+"/report", strings.NewReader(tc.body))
			resp, err := newUnixClient(SockPath(workDir)).Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestPostBridgeReportSurfacesDaemonError(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, []byte(`{"type":"bogus"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown type") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v, want the daemon's 400 message", err)
	}
}

func TestOpenBridgeStreamSurfacesDaemonError(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	body, err := OpenBridgeStream(bridgeTestCtx(t), workDir, "bad name")
	if err == nil {
		body.Close()
		t.Fatal("OpenBridgeStream accepted an invalid agent")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v, want the daemon's status", err)
	}
}

func TestServerDefaultsToItsOwnBridge(t *testing.T) {
	s := New(tmpSockPath(t, "b.sock"), "/tmp/leo.yaml", nil)
	if s.Bridge() == nil {
		t.Fatal("a daemon built without WithBridge must still serve a hub")
	}
	hub := bridge.New(bridge.Options{})
	if got := New(tmpSockPath(t, "c.sock"), "/tmp/leo.yaml", nil, WithBridge(hub)).Bridge(); got != hub {
		t.Fatal("WithBridge hub was not the one the daemon serves")
	}
}
