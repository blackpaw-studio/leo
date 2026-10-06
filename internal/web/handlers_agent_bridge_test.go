package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// testBridgeLaunch is the launch a bridgedAgent's key is open under.
const testBridgeLaunch = "launch-test"

// bridgedAgent routes agent name to a hub whose mod (the test) holds the
// stream open, and fails the test on any tmux exec.
type bridgedAgent struct {
	t      *testing.T
	hub    *bridge.Hub
	key    string
	stream *bridge.Stream

	mu        sync.Mutex
	tmuxCalls [][]string
}

func newBridgedAgent(t *testing.T, s *Server, name string, connected bool) *bridgedAgent {
	t.Helper()
	b := &bridgedAgent{t: t, hub: bridge.New(bridge.Options{}), key: name + ".k1"}
	t.Cleanup(b.hub.Close)
	target, err := b.hub.Open(b.key, testBridgeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	s.bridgeRouter = &bridge.Router{Hub: b.hub, Targets: func(n string) (bridge.Target, bool) { return target, n == name }}
	if connected {
		stream, err := b.hub.Connect(b.key, testBridgeLaunch)
		if err != nil {
			t.Fatal(err)
		}
		b.stream = stream
	}
	s.execCommand = func(_ string, args ...string) *exec.Cmd {
		b.mu.Lock()
		b.tmuxCalls = append(b.tmuxCalls, args)
		b.mu.Unlock()
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ typed")
		}
		return exec.Command("true")
	}
	return b
}

// next reads the mod's next command.
func (b *bridgedAgent) next() bridge.Command {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := b.stream.Next(ctx)
	if err != nil {
		b.t.Fatalf("stream.Next: %v", err)
	}
	return cmd
}

func (b *bridgedAgent) ack(cmd bridge.Command, ok bool, msg string) {
	b.t.Helper()
	if err := b.hub.Apply(b.key, testBridgeLaunch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: ok, Error: msg}); err != nil {
		b.t.Fatal(err)
	}
}

// serveAsync runs req against s in the background; the mod side answers on
// the test goroutine.
func serveAsync(s *Server, method, path, body string) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		out <- w
	}()
	return out
}

func (b *bridgedAgent) tmuxCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.tmuxCalls)
}

// leo_send_message to a bridged agent arrives as a non-user deliver naming
// the sending agent; the "[message from x]" wire prefix becomes that frame.
func TestWebAgentMessageFramesAgentMessagesOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	pub := &recordingPublisher{}
	s.publisher = pub
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"[message from bob] hi\nthere","from":"bob"}`)
	cmd := b.next()
	if cmd.Op != bridge.OpDeliver || cmd.AsUser || cmd.Text != "From agent bob via leo:\n\nhi\nthere" {
		t.Fatalf("deliver = %+v", cmd)
	}
	b.ack(cmd, true, "")
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatalf("bridged message touched tmux %d times", n)
	}
	if msgs := pub.messages(); len(msgs) != 1 || msgs[0].From != "bob" || msgs[0].To != "assistant" {
		t.Fatalf("published = %+v", msgs)
	}
}

// A message from a human (no sender) is the user's own prompt, verbatim.
func TestWebAgentMessageFromAHumanIsTheUserOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"status please"}`)
	cmd := b.next()
	if cmd.Op != bridge.OpDeliver || !cmd.AsUser || cmd.Text != "status please" {
		t.Fatalf("deliver = %+v", cmd)
	}
	b.ack(cmd, true, "")
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

// A busy agent acks a deliver only once its turn ends. The send is answered
// 202 (queued, not yet accepted) rather than held or failed — a retry would
// deliver twice — and the message is announced once it is accepted.
func TestWebAgentMessageQueuedOnABusyBridgeIsAcceptedThenAnnounced(t *testing.T) {
	s, _ := newTestServer(t)
	pub := &recordingPublisher{}
	s.publisher = pub
	old := bridgeMessageWait
	bridgeMessageWait = 30 * time.Millisecond
	t.Cleanup(func() { bridgeMessageWait = old })
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"[message from bob] later","from":"bob"}`)
	cmd := b.next()
	w := <-done
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "queued") {
		t.Fatalf("status = %d, body = %s; want 202 queued", w.Code, w.Body.String())
	}
	if len(pub.messages()) != 0 {
		t.Fatal("queued message announced before it was accepted")
	}
	b.ack(cmd, true, "")
	deadline := time.Now().Add(5 * time.Second)
	for len(pub.messages()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("accepted message never announced")
		}
		time.Sleep(time.Millisecond)
	}
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatalf("queued message was also typed via tmux (%d calls)", n)
	}
}

func TestWebAgentMessageRejectedByTheBridgeIsAnError(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"hi"}`)
	b.ack(b.next(), false, "prompt rejected")
	w := <-done
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "prompt rejected") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatal("a rejected bridge deliver fell back to tmux")
	}
}

// An agent launched with the bridge whose mod is not connected right now is
// messaged the old way.
func TestWebAgentMessageWithADisconnectedBridgeUsesTmux(t *testing.T) {
	s, _ := newTestServer(t)
	s.resolvePeerSocket = func(context.Context, string) (string, error) { return "", context.Canceled }
	old := messageInputPoll
	messageInputPoll = time.Millisecond
	t.Cleanup(func() { messageInputPoll = old })
	b := newBridgedAgent(t, s, "assistant", false)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/web/agent/assistant/message", strings.NewReader(`{"text":"hi"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if b.tmuxCallCount() == 0 {
		t.Fatal("disconnected bridge did not fall back to tmux")
	}
}

func TestWebAgentInterruptOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/interrupt", "")
	cmd := b.next()
	if cmd.Op != bridge.OpInterrupt {
		t.Fatalf("command = %+v, want interrupt", cmd)
	}
	b.ack(cmd, true, "")
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatalf("bridged interrupt also sent Escape via tmux (%d calls)", n)
	}
}

func TestWebAgentInterruptBridgeFailureIsAnError(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/interrupt", "")
	b.ack(b.next(), false, "no turn")
	if w := <-done; w.Code < 400 {
		t.Fatalf("status = %d, want an error", w.Code)
	}
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatal("a failed bridge interrupt fell back to tmux")
	}
}

// Compact and clear wait for the agent's turn to end, and an agent calls
// them on itself mid-turn, so they are queued and answered 202 at once.
func TestWebAgentCompactAndClearQueueOnTheBridge(t *testing.T) {
	for _, tc := range []struct{ verb, op string }{{"compact", bridge.OpCompact}, {"clear", bridge.OpClear}} {
		t.Run(tc.verb, func(t *testing.T) {
			s, _ := newTestServer(t)
			b := newBridgedAgent(t, s, "assistant", true)
			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/web/agent/assistant/"+tc.verb, nil))
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if cmd := b.next(); cmd.Op != tc.op {
				t.Fatalf("command = %+v, want %s", cmd, tc.op)
			}
			if n := b.tmuxCallCount(); n != 0 {
				t.Fatalf("bridged %s touched tmux", tc.verb)
			}
		})
	}
}

// Without a live bridge, compact and clear type the slash command.
func TestWebAgentCompactWithoutABridgeTypesTheCommand(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", false)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/web/agent/assistant/compact", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var typed strings.Builder
	b.mu.Lock()
	for _, c := range b.tmuxCalls {
		if argsContain(c, "send-keys") {
			typed.WriteString(c[len(c)-1])
		}
	}
	b.mu.Unlock()
	if typed.String() != "/compactEnter" {
		t.Fatalf("typed %q, want /compact then Enter", typed.String())
	}
}

func TestAgentTokenMayCompactAndClear(t *testing.T) {
	for _, path := range []string{"/web/agent/alpha/compact", "/web/agent/alpha/clear"} {
		if !agentCallableBrowserPath(path) {
			t.Errorf("%s is not agent-callable", path)
		}
	}
}

// An ack that settled the send by the time the wait ran out is an answer,
// not a "queued": the timer firing must not hide it.
func TestBridgeSendPrefersAnOutcomeThatBeatTheTimer(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	timer := make(chan time.Time, 1)
	orig := bridgeSendTimer
	bridgeSendTimer = func(time.Duration) (<-chan time.Time, func() bool) { return timer, func() bool { return true } }
	t.Cleanup(func() { bridgeSendTimer = orig })

	target, ok := s.bridgeRouter.Route("assistant")
	if !ok {
		t.Fatal("no route")
	}
	returned := make(chan struct{})
	type answer struct {
		accepted bool
		err      error
	}
	got := make(chan answer, 1)
	go func() {
		defer close(returned)
		// onAck runs once the send has settled: the wait runs out right
		// then, and the caller answers before the sender finishes up.
		accepted, err := s.bridgeSend(target, "test", bridge.Deliver("hi", true), time.Hour, func() {
			timer <- time.Now()
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
			}
		})
		got <- answer{accepted, err}
	}()
	b.ack(b.next(), true, "")
	a := <-got
	if a.err != nil || !a.accepted {
		t.Fatalf("bridgeSend = %v, %v; want accepted, the ack had landed", a.accepted, a.err)
	}
}

// durably makes b's router keep delivers as the daemon's does: through
// Router.Queue, recording who each was queued for and from.
func (b *bridgedAgent) durably(s *Server) *[]string {
	queued := &[]string{}
	var mu sync.Mutex
	s.bridgeRouter.Queue = func(agent string, t bridge.Target, cmd bridge.Command, from string) (*bridge.Ticket, error) {
		mu.Lock()
		*queued = append(*queued, agent+"<"+from)
		mu.Unlock()
		return b.hub.EnqueueTo(t, cmd)
	}
	return queued
}

// A message is queued durably, naming its agent and sender.
func TestWebAgentMessageIsQueuedDurably(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	queued := b.durably(s)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"[message from bob] hi","from":"bob"}`)
	b.ack(b.next(), true, "")
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if strings.Join(*queued, ",") != "assistant<bob" {
		t.Fatalf("queued %q, want once for assistant from bob", *queued)
	}
}

// A durable message whose launch ends before the mod takes it is not lost:
// it waits for the agent's next launch, so it is answered queued (202), not
// failed, which would invite a resend that delivers it twice.
func TestWebAgentMessageWhoseLaunchEndsFirstStaysQueued(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	b.durably(s)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"hi"}`)
	b.next()
	b.hub.Forget(b.key)
	w := <-done
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "queued") {
		t.Fatalf("status = %d, body = %s; want 202 queued", w.Code, w.Body.String())
	}
}

// Without durable delivery the same loss is the sender's error, as before.
func TestWebAgentMessageWhoseLaunchEndsFirstFailsWithoutAnOutbox(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/web/agent/assistant/message", `{"text":"hi"}`)
	b.next()
	b.hub.Forget(b.key)
	if w := <-done; w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want 500", w.Code, w.Body.String())
	}
}
