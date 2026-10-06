package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

const (
	bridgeAgent  = "leo-alpha"
	bridgeLaunch = "launch-test"
)

// startBridgeServer starts a daemon whose bridge hub is the one returned, so
// a test can drive the outbox directly and observe it over the socket.
// bridgeAgent's key is open under bridgeLaunch, as a launch's would be.
func startBridgeServer(t *testing.T, configure ...func(*Server)) (string, *bridge.Hub, *Server) {
	t.Helper()
	workDir := tmpWorkDir(t)
	hub := bridge.New(bridge.Options{})
	if _, err := hub.Open(bridgeAgent, bridgeLaunch); err != nil {
		t.Fatal(err)
	}
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
func openStream(t *testing.T, workDir string, hub *bridge.Hub, agent string, launch ...string) *bufio.Reader {
	t.Helper()
	ctx := bridgeTestCtx(t)
	l := bridgeLaunch
	if len(launch) > 0 {
		l = launch[0]
	}
	body, err := OpenBridgeStream(ctx, workDir, agent, l)
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
	req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodGet, "http://daemon/api/bridge/"+bridgeAgent+"/stream?launch="+bridgeLaunch, nil)
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
	newer, err := OpenBridgeStream(ctx, workDir, bridgeAgent, bridgeLaunch)
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
	body, err := OpenBridgeStream(ctx, workDir, bridgeAgent, bridgeLaunch)
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
	ctx := bridgeTestCtx(t)
	done := make(chan error, 1)
	go func() { done <- hub.Send(ctx, bridgeAgent, bridge.Deliver("hi", false)) }()

	var cmd struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(readLine(t, r)), &cmd); err != nil || cmd.ID == "" {
		t.Fatalf("stream line has no command id (err %v)", err)
	}
	if err := PostBridgeReport(ctx, workDir, bridgeAgent, bridgeLaunch, []byte(`{"type":"ack","id":"`+cmd.ID+`","ok":true}`)); err != nil {
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
	if err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch, []byte(`{"type":"event","name":"turn.start"}`)); err != nil {
		t.Fatalf("PostBridgeReport: %v", err)
	}
	if !hub.State(bridgeAgent).Busy {
		t.Fatal("turn.start report did not mark the agent busy")
	}
}

// A turn.complete carries the agent's whole final message and a turn.start
// its whole prompt (an opening brief can pass 96 KiB), so reports of a few
// MiB must go through.
func TestBridgeReportAcceptsLargeTurnText(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	message := strings.Repeat("m", 4<<20)
	body := `{"type":"event","name":"turn.complete","message":"` + message + `"}`
	if err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch, []byte(body)); err != nil {
		t.Fatalf("PostBridgeReport of a %d-byte report: %v", len(body), err)
	}
	if hub.State(bridgeAgent).LastTurnComplete.IsZero() {
		t.Fatal("the large turn.complete was not applied")
	}
}

func TestBridgeReportRejectsBadRequests(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	turnStart := `{"type":"event","name":"turn.start"}`
	cases := []struct {
		name   string
		agent  string
		launch string
		body   string
		status int
	}{
		{"unknown type", bridgeAgent, bridgeLaunch, `{"type":"bogus"}`, http.StatusBadRequest},
		{"not json", bridgeAgent, bridgeLaunch, `nope`, http.StatusBadRequest},
		{"unknown field", bridgeAgent, bridgeLaunch, `{"type":"event","name":"turn.start","x":1}`, http.StatusBadRequest},
		{"invalid agent", "bad%20name", bridgeLaunch, turnStart, http.StatusBadRequest},
		{"no launch", bridgeAgent, "", turnStart, http.StatusBadRequest},
		{"invalid launch", bridgeAgent, "a%2Fb", turnStart, http.StatusBadRequest},
		{"oversized", bridgeAgent, bridgeLaunch, `{"type":"event","name":"turn.complete","usage":{"pad":"` + strings.Repeat("x", int(MaxBridgeReportBytes)) + `"}}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodPost,
				"http://daemon/api/bridge/"+tc.agent+"/report?launch="+tc.launch, strings.NewReader(tc.body))
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
	err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch, []byte(`{"type":"bogus"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown type") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v, want the daemon's 400 message", err)
	}
}

func TestOpenBridgeStreamSurfacesDaemonError(t *testing.T) {
	workDir, _, _ := startBridgeServer(t)
	body, err := OpenBridgeStream(bridgeTestCtx(t), workDir, "bad name", bridgeLaunch)
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

// A report or connection for a forgotten key comes from a launch that is
// over: the daemon answers 410 Gone, which the client surfaces as
// ErrBridgeGone so the mod can stop retrying a moot report.
func TestBridgeRefusesAForgottenKeyWithGone(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	hub.Forget(bridgeAgent)

	err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch, []byte(`{"type":"event","name":"turn.start"}`))
	if !errors.Is(err, ErrBridgeGone) {
		t.Fatalf("report for a forgotten key: err=%v, want ErrBridgeGone", err)
	}
	if hub.State(bridgeAgent).Busy {
		t.Fatal("a forgotten key's late report was applied")
	}
	body, err := OpenBridgeStream(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch)
	if err == nil {
		body.Close()
		t.Fatal("OpenBridgeStream connected a forgotten key")
	}
	if !errors.Is(err, ErrBridgeGone) {
		t.Fatalf("stream for a forgotten key: err=%v, want ErrBridgeGone", err)
	}
}

// A report or connection from any launch but the key's current one (a
// relaunched agent's predecessor, or a session a restarted daemon has not
// adopted yet) gets 409 Conflict, which the client surfaces as
// ErrBridgeStale: the report is moot and must not be retried.
func TestBridgeRefusesAStaleLaunchWithConflict(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	if _, err := hub.Open(bridgeAgent, "launch-successor"); err != nil {
		t.Fatal(err)
	}

	err := PostBridgeReport(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch, []byte(`{"type":"event","name":"turn.start"}`))
	if !errors.Is(err, ErrBridgeStale) || !strings.Contains(err.Error(), "409") {
		t.Fatalf("report from the predecessor: err=%v, want ErrBridgeStale (409)", err)
	}
	if hub.State(bridgeAgent).Busy {
		t.Fatal("the predecessor's report marked the successor busy")
	}
	body, err := OpenBridgeStream(bridgeTestCtx(t), workDir, bridgeAgent, bridgeLaunch)
	if err == nil {
		body.Close()
		t.Fatal("OpenBridgeStream connected the predecessor")
	}
	if !errors.Is(err, ErrBridgeStale) {
		t.Fatalf("stream for the predecessor: err=%v, want ErrBridgeStale", err)
	}
	if hub.Connected(bridgeAgent) {
		t.Fatal("the predecessor holds the successor's stream")
	}
	_ = openStream(t, workDir, hub, bridgeAgent, "launch-successor")
}

// A key no launch opened may be a surviving session the daemon has not
// adopted yet: its mod gets 503 (ErrBridgeNotReady) and retries, never a
// 409 that would make it drop a report or stop. Once the daemon has said
// which keys it adopts, any other unopened key is a 409.
func TestBridgeTellsAModAwaitingAdoptionToRetry(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	const key = "leo-surviving"
	expect := func(want error, status string) {
		t.Helper()
		err := PostBridgeReport(bridgeTestCtx(t), workDir, key, "launch-old", []byte(`{"type":"event","name":"turn.start"}`))
		if !errors.Is(err, want) || !strings.Contains(err.Error(), status) {
			t.Fatalf("report: err=%v, want %v (%s)", err, want, status)
		}
		body, err := OpenBridgeStream(bridgeTestCtx(t), workDir, key, "launch-old")
		if err == nil {
			body.Close()
			t.Fatal("OpenBridgeStream connected an unopened key")
		}
		if !errors.Is(err, want) {
			t.Fatalf("stream: err=%v, want %v", err, want)
		}
	}
	expect(ErrBridgeNotReady, "503")
	hub.AwaitAdoption([]string{key})
	expect(ErrBridgeNotReady, "503")
	hub.EndAdoption(key)
	expect(ErrBridgeStale, "409")
}
