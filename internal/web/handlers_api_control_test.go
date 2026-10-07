package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

var apiControlVerbs = []string{"message", "interrupt", "compact", "clear"}

// The control API is operator-only: an agent's own token — which a
// transcript can leak — must not let it drive other agents through
// /api/v1, even though it reaches the rest of /api/*.
func TestAPIControlRejectsTheAgentToken(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, verb := range apiControlVerbs {
		path := "/api/v1/agents/assistant/" + verb
		if w := requestAs(t, s, "POST", path, testAgentToken, `{"text":"hi"}`); w.Code != http.StatusForbidden {
			t.Errorf("POST %s with agent token = %d, want 403", path, w.Code)
		}
		if w := requestAs(t, s, "POST", path, "", `{"text":"hi"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", path, w.Code)
		}
		// The operator gets through auth to the handler, which knows no
		// such agent on this server.
		if w := requestAs(t, s, "POST", path, testAPIToken, `{"text":"hi"}`); w.Code != http.StatusNotFound {
			t.Errorf("POST %s with operator token = %d, want 404; body = %s", path, w.Code, w.Body.String())
		}
	}
}

// apiControlResponse decodes the /api envelope of a control response.
type apiControlResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Data  struct {
		Transport string `json:"transport"`
		Queued    bool   `json:"queued"`
	} `json:"data"`
}

func decodeControl(t *testing.T, w *httptest.ResponseRecorder) apiControlResponse {
	t.Helper()
	var resp apiControlResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return resp
}

func postControl(s *Server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return w
}

func TestAPIControlMessageOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/api/v1/agents/assistant/message", `{"text":"status please","from":"ops"}`)
	cmd := b.next()
	if cmd.Op != bridge.OpDeliver || cmd.AsUser || cmd.Text != "From agent ops via leo:\n\nstatus please" {
		t.Fatalf("deliver = %+v", cmd)
	}
	b.ack(cmd, true, "")
	w := <-done
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "bridge" {
		t.Fatalf("response = %+v", resp)
	}
	if n := b.tmuxCallCount(); n != 0 {
		t.Fatalf("bridged message touched tmux %d times", n)
	}
}

func TestAPIControlMessageQueuedOnABusyBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	orig := bridgeMessageWait
	bridgeMessageWait = 10 * time.Millisecond
	t.Cleanup(func() { bridgeMessageWait = orig })
	done := serveAsync(s, "POST", "/api/v1/agents/assistant/message", `{"text":"hi"}`)
	cmd := b.next()
	w := <-done
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || !resp.Data.Queued {
		t.Fatalf("response = %+v", resp)
	}
	b.ack(cmd, true, "")
}

// recordTmux stubs s's tmux, resolving every pane to %7 and echoing typed
// input back from capture-pane, and returns the argv of every call.
func recordTmux(s *Server) func() [][]string {
	var mu sync.Mutex
	var calls [][]string
	s.execCommand = func(_ string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		switch {
		case argsContain(args, "list-panes"):
			return exec.Command("echo", "%7")
		case argsContain(args, "capture-pane"):
			return exec.Command("echo", "❯ typed")
		}
		return exec.Command("true")
	}
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), calls...)
	}
}

// sendKeys keeps only the send-keys calls of calls.
func sendKeys(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if argsContain(c, "send-keys") {
			out = append(out, c)
		}
	}
	return out
}

func keys(args ...string) []string {
	return append([]string{"-L", "leo", "send-keys", "-t", "%7"}, args...)
}

func TestAPIControlMessageWithoutABridgePastesIntoTmux(t *testing.T) {
	s, _ := newTestServer(t)
	shrinkMessagePoll(t)
	calls := recordTmux(s)
	w := postControl(s, "/api/v1/agents/assistant/message", `{"text":"Enter; C-c"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "legacy" {
		t.Fatalf("response = %+v", resp)
	}
	want := [][]string{keys("-l", "Enter; C-c"), keys("Enter")}
	if got := sendKeys(calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys argv = %q, want %q", got, want)
	}
}

func shrinkMessagePoll(t *testing.T) {
	t.Helper()
	orig := messageInputPoll
	messageInputPoll = time.Millisecond
	t.Cleanup(func() { messageInputPoll = orig })
}

func TestAPIControlInterruptOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/api/v1/agents/assistant/interrupt", "")
	cmd := b.next()
	if cmd.Op != bridge.OpInterrupt {
		t.Fatalf("command = %+v", cmd)
	}
	b.ack(cmd, true, "")
	w := <-done
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "bridge" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAPIControlInterruptBridgeFailureIsBadGateway(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := serveAsync(s, "POST", "/api/v1/agents/assistant/interrupt", "")
	b.ack(b.next(), false, "no turn")
	w := <-done
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); resp.OK || !strings.Contains(resp.Error, "no turn") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAPIControlInterruptWithoutABridgeSendsEscapes(t *testing.T) {
	s, _ := newTestServer(t)
	orig := interruptDelayedAttempts
	interruptDelayedAttempts = 0
	t.Cleanup(func() { interruptDelayedAttempts = orig })
	burst := make(chan struct{})
	s.afterInterruptBurst = func() { close(burst) }
	calls := recordTmux(s)
	w := postControl(s, "/api/v1/agents/assistant/interrupt", "")
	<-burst
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "legacy" {
		t.Fatalf("response = %+v", resp)
	}
	want := [][]string{keys("Escape"), keys("Escape"), keys("Escape")}
	if got := sendKeys(calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys argv = %q, want %q", got, want)
	}
}

func TestAPIControlCompactOverTheBridgeCarriesInstructions(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	w := postControl(s, "/api/v1/agents/assistant/compact", `{"instructions":"keep the plan"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || !resp.Data.Queued {
		t.Fatalf("response = %+v", resp)
	}
	if cmd := b.next(); cmd.Op != bridge.OpCompact || cmd.Instructions != "keep the plan" {
		t.Fatalf("command = %+v", cmd)
	}
}

// Without a bridge the slash command is typed key by key (so claude's
// slash menu engages), instructions follow as one literal paste on a
// single line — a typed newline would submit early — then Enter.
func TestAPIControlCompactWithoutABridgeTypesCommandAndInstructions(t *testing.T) {
	s, _ := newTestServer(t)
	calls := recordTmux(s)
	w := postControl(s, "/api/v1/agents/assistant/compact", `{"instructions":"keep\nthe  plan;"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "legacy" {
		t.Fatalf("response = %+v", resp)
	}
	var want [][]string
	for _, ch := range "/compact" {
		want = append(want, keys(string(ch)))
	}
	want = append(want, keys("-l", " keep the plan;"), keys("Enter"))
	if got := sendKeys(calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys argv = %q, want %q", got, want)
	}
}

func TestAPIControlClearWithoutABridgeTypesTheCommand(t *testing.T) {
	s, _ := newTestServer(t)
	calls := recordTmux(s)
	w := postControl(s, "/api/v1/agents/assistant/clear", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var want [][]string
	for _, ch := range "/clear" {
		want = append(want, keys(string(ch)))
	}
	want = append(want, keys("Enter"))
	if got := sendKeys(calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("send-keys argv = %q, want %q", got, want)
	}
}

func TestAPIControlClearOverTheBridge(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	w := postControl(s, "/api/v1/agents/assistant/clear", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if cmd := b.next(); cmd.Op != bridge.OpClear {
		t.Fatalf("command = %+v", cmd)
	}
}

func TestAPIControlRejectsBadRequests(t *testing.T) {
	big := strings.Repeat("x", maxControlMessageBytes+1)
	bigInstructions := strings.Repeat("x", maxCompactInstructionsBytes+1)
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"bad json", "/api/v1/agents/assistant/message", `{"text":`, http.StatusBadRequest},
		{"empty text", "/api/v1/agents/assistant/message", `{"text":""}`, http.StatusBadRequest},
		{"no body", "/api/v1/agents/assistant/message", ``, http.StatusBadRequest},
		{"oversized text", "/api/v1/agents/assistant/message", `{"text":"` + big + `"}`, http.StatusRequestEntityTooLarge},
		{"oversized body", "/api/v1/agents/assistant/message", `{"text":"hi","pad":"` + strings.Repeat("x", 4*maxControlMessageBytes) + `"}`, http.StatusRequestEntityTooLarge},
		{"compact bad json", "/api/v1/agents/assistant/compact", `{`, http.StatusBadRequest},
		{"oversized instructions", "/api/v1/agents/assistant/compact", `{"instructions":"` + bigInstructions + `"}`, http.StatusRequestEntityTooLarge},
		{"unknown agent message", "/api/v1/agents/ghost/message", `{"text":"hi"}`, http.StatusNotFound},
		{"unknown agent interrupt", "/api/v1/agents/ghost/interrupt", ``, http.StatusNotFound},
		{"unknown agent compact", "/api/v1/agents/ghost/compact", ``, http.StatusNotFound},
		{"unknown agent clear", "/api/v1/agents/ghost/clear", ``, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			calls := recordTmux(s)
			w := postControl(s, tc.path, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}
			if resp := decodeControl(t, w); resp.OK || resp.Error == "" {
				t.Fatalf("response = %+v, want an error envelope", resp)
			}
			if got := sendKeys(calls()); len(got) != 0 {
				t.Fatalf("rejected request typed %q", got)
			}
		})
	}
}

// ControlHandler serves the same operations, unauthenticated, under the
// daemon socket's /agents/{name}/… shape: the socket's 0600 mode is its
// auth.
func TestControlHandlerServesSocketRoutes(t *testing.T) {
	s, _ := newTestServer(t)
	calls := recordTmux(s)
	h := s.ControlHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/agents/assistant/clear", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp := decodeControl(t, w); !resp.OK || resp.Data.Transport != "legacy" {
		t.Fatalf("response = %+v", resp)
	}
	if got := sendKeys(calls()); len(got) == 0 {
		t.Fatal("clear typed nothing")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/agents/ghost/interrupt", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent status = %d", w.Code)
	}
}
