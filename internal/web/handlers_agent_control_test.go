package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// argsContain reports whether a tmux arg slice includes sub (e.g. "capture-pane").
func argsContain(args []string, sub string) bool {
	for _, a := range args {
		if a == sub {
			return true
		}
	}
	return false
}

func TestWebAgentMessageSendsLiteralThenEnter(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll := messageInputPoll
	messageInputPoll = time.Millisecond
	defer func() { messageInputPoll = oldPoll }()

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ Enter the build status please")
		}
		return exec.Command("true") // harmless no-op
	}

	body := strings.NewReader(`{"text":"Enter the build status please"}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// calls[0] is the pane-resolution list-panes call; calls[1] is the
	// literal send.
	first := strings.Join(calls[1], " ")
	if !strings.Contains(first, "send-keys") || !strings.Contains(first, "-l") ||
		!strings.Contains(first, "leo-assistant") || !strings.Contains(first, "Enter the build status please") {
		t.Errorf("second call should be literal send to leo-assistant; got %v", calls[1])
	}
	last := calls[len(calls)-1]
	if last[len(last)-1] != "Enter" {
		t.Errorf("last call should submit with Enter; got %v", last)
	}
}

// TestWebAgentMessageConfirmsInputBeforeEnter guards the fix for the
// intermittent "Enter not registered" bug: an Enter fired in the same input
// burst as the literal text is treated by claude's Ink REPL as a newline, not
// a submit, leaving the message unsent. The handler must capture-pane and
// confirm the typed text landed in the input box before sending Enter.
func TestWebAgentMessageConfirmsInputBeforeEnter(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll := messageInputPoll
	messageInputPoll = time.Millisecond
	defer func() { messageInputPoll = oldPoll }()

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ hello there") // text landed
		}
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"hello there"}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// calls[0] is the pane-resolution list-panes call; calls[1] is the
	// literal text send.
	if !argsContain(calls[1], "-l") || !argsContain(calls[1], "hello there") {
		t.Errorf("second call should be literal text send; got %v", calls[1])
	}
	// Last call: Enter submit.
	last := calls[len(calls)-1]
	if last[len(last)-1] != "Enter" {
		t.Errorf("last call should submit with Enter; got %v", last)
	}
	// A capture-pane must occur between the literal send and the Enter — the
	// confirmation that breaks the paste/submit race.
	enterIdx := len(calls) - 1
	sawCaptureBeforeEnter := false
	for i := 1; i < enterIdx; i++ {
		if argsContain(calls[i], "capture-pane") {
			sawCaptureBeforeEnter = true
		}
	}
	if !sawCaptureBeforeEnter {
		t.Errorf("handler must capture-pane to confirm input before Enter; calls=%v", calls)
	}
}

// TestWebAgentMessageFallsOpenWhenInputNeverConfirms ensures a message is
// never silently dropped: if the input box never reflects the typed text
// within the bounded poll window (busy/mid-turn or unreadable pane), the
// handler still submits with Enter rather than hanging or skipping the send.
func TestWebAgentMessageFallsOpenWhenInputNeverConfirms(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll, oldAttempts := messageInputPoll, messageInputAttempts
	messageInputPoll, messageInputAttempts = time.Millisecond, 3
	defer func() { messageInputPoll, messageInputAttempts = oldPoll, oldAttempts }()

	var calls [][]string
	captureCount := 0
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if argsContain(args, "capture-pane") {
			captureCount++
			return exec.Command("echo", "❯ ") // empty input box — never confirms
		}
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"hi"}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// Polling is bounded — must not exceed the attempt budget.
	if captureCount > messageInputAttempts {
		t.Errorf("capture-pane polled %d times, want <= %d", captureCount, messageInputAttempts)
	}
	// Falls open: Enter is still sent so the message is delivered.
	last := calls[len(calls)-1]
	if last[len(last)-1] != "Enter" {
		t.Errorf("handler should fall open and still submit with Enter; got %v", last)
	}
}

func TestWebAgentMessageUnknownTargetListsRecipients(t *testing.T) {
	s, _ := newTestServer(t) // mock has process "assistant"

	body := strings.NewReader(`{"text":"hi"}`)
	req := httptest.NewRequest("POST", "/web/agent/ghost/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "assistant") {
		t.Errorf("not-found error should list recipients; got %s", w.Body.String())
	}
}

func TestWebAgentMessageRejectsEmptyText(t *testing.T) {
	s, _ := newTestServer(t)
	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// TestWebAgentMessageLeoPrefixedTargetUsesSingleLeoPrefix guards against the
// double-prefix bug: agents whose canonical name already starts with "leo-"
// (e.g. renamed agents and auto-named leo-coding-* agents) have a tmux session
// named exactly after their canonical name, not "leo-"+name. The handler must
// resolve the session via agent.SessionName, which keeps a single prefix.
func TestWebAgentMessageLeoPrefixedTargetUsesSingleLeoPrefix(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll, oldAttempts := messageInputPoll, messageInputAttempts
	messageInputPoll, messageInputAttempts = time.Millisecond, 2
	defer func() { messageInputPoll, messageInputAttempts = oldPoll, oldAttempts }()

	s.processes.(*mockProcesses).states["leo-coding-foo"] = ProcessStateInfo{
		Name:   "leo-coding-foo",
		Status: "running",
	}

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest("POST", "/web/agent/leo-coding-foo/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(calls) == 0 {
		t.Fatal("expected at least one tmux call")
	}
	for i, call := range calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "leo-leo-coding-foo") {
			t.Errorf("call %d targets double-prefixed session: %v", i, call)
		}
		if !strings.Contains(joined, "leo-coding-foo") {
			t.Errorf("call %d should target leo-coding-foo: %v", i, call)
		}
	}
}

// TestWebAgentMessageAutoWakesSuspendedAgent verifies that when a message is
// targeted at a name that is NOT in the live process states but IS a
// dormant, wakeable (WakeOnMessage=true) agent, handleWebAgentMessage calls
// Start and then delivers via the readiness-probing InjectPrompt path — NOT
// the 2s fast-path send-keys.
//
// A just-started claude takes tens of seconds to boot before its input box
// accepts input. Falling through to send-keys (which only waits ~2s) would
// silently drop the first post-wake message.
func TestWebAgentMessageAutoWakesSuspendedAgent(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	// "suspended-worker" is NOT in live states — it must be started then
	// delivered via injectPrompt (readiness-probing), not the fast-path.
	svc.wakeableNames = map[string]bool{"suspended-worker": true}

	// Capture what injectPrompt was called with. Delivery is asynchronous (the
	// handler resumes, then injects in a goroutine so a ~60s cold boot doesn't
	// exceed the HTTP write timeout), so hand the values back over a channel and
	// wait for them rather than reading shared vars (which would race).
	type injectArgs struct{ session, body string }
	injected := make(chan injectArgs, 1)
	s.injectPrompt = func(ctx context.Context, session, body string) error {
		injected <- injectArgs{session, body}
		return nil
	}

	// Track execCommand calls to assert the fast send-keys path was NOT used.
	var execCalls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		execCalls = append(execCalls, args)
		return exec.Command("true")
	}

	reqBody := strings.NewReader(`{"text":"wake up and do the thing"}`)
	req := httptest.NewRequest("POST", "/web/agent/suspended-worker/message", reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	// Async delivery returns promptly with 202 Accepted (not held for the boot).
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// Start must have been called for the wakeable agent (synchronous).
	if !svc.resumeCalled {
		t.Fatal("expected Start to be called for a wakeable agent")
	}
	if svc.resumeName != "suspended-worker" {
		t.Errorf("expected Start called with 'suspended-worker', got %q", svc.resumeName)
	}

	// The readiness-probing injector must be called (asynchronously) with the
	// correct session name and message body.
	var got injectArgs
	select {
	case got = <-injected:
	case <-time.After(2 * time.Second):
		t.Fatal("injectPrompt was not called within timeout (async delivery)")
	}
	wantSession := agent.SessionName("suspended-worker")
	if got.session != wantSession {
		t.Errorf("injectPrompt session = %q, want %q", got.session, wantSession)
	}
	if got.body != "wake up and do the thing" {
		t.Errorf("injectPrompt body = %q, want %q", got.body, "wake up and do the thing")
	}

	// The fast send-keys path must NOT have been used for the resumed case —
	// it would silently drop the message before claude finishes booting.
	for _, call := range execCalls {
		if argsContain(call, "send-keys") && argsContain(call, "-l") {
			t.Errorf("fast-path send-keys must not fire for a just-resumed agent; got call=%v", call)
		}
	}
}

// TestWebAgentMessageUnknownTargetWithAgentServiceStill404 verifies that a
// name that is neither live NOR wakeable returns 404, and Start is never
// attempted for it — Wakeable already reports false for a name with no
// dormant record.
func TestWebAgentMessageUnknownTargetWithAgentServiceStill404(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest("POST", "/web/agent/ghost/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if svc.resumeCalled {
		t.Fatal("Start must not be attempted for an unknown, non-wakeable target")
	}
	body2 := w.Body.String()
	if !strings.Contains(body2, "ghost") {
		t.Errorf("404 body should mention the unknown name; got %s", body2)
	}
}

// TestWebAgentMessageDoesNotWakeManuallyStoppedAgent is the load-bearing
// auto-wake guard on the channel path: a dormant agent with
// WakeOnMessage=false (an operator-initiated stop, not the idle sweep) must
// NOT be woken by an inbound message — Wakeable reports false for it even
// though a record exists, and the handler must fall through to the same 404
// an unknown name gets, never calling Start.
func TestWebAgentMessageDoesNotWakeManuallyStoppedAgent(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	// A record exists (tracked via records in other tests), but it is NOT
	// wakeable — the manual-stop case.
	svc.wakeableNames = map[string]bool{"stopped-worker": false}

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest("POST", "/web/agent/stopped-worker/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if svc.resumeCalled {
		t.Fatal("Start must never be called for a manually stopped (non-wakeable) agent")
	}
}

// --- non-claude dispatch (SessionDriver routing) ---

// fakeTurnsDriver is a minimal harness.SessionDriver whose Inject records
// every call instead of touching any real process. Used to verify
// handleWebAgentMessage routes non-claude targets through the driver instead
// of tmux.
type fakeTurnsDriver struct {
	mu      sync.Mutex
	injects []fakeInjectCall
	result  *harness.Result
	err     error
}

type fakeInjectCall struct {
	handle harness.SessionHandle
	msg    string
}

func (d *fakeTurnsDriver) Start(context.Context, harness.SessionHandle) error { return nil }
func (d *fakeTurnsDriver) Inject(_ context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	d.mu.Lock()
	d.injects = append(d.injects, fakeInjectCall{handle: h, msg: msg})
	d.mu.Unlock()
	return d.result, d.err
}
func (d *fakeTurnsDriver) Attach(harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{}, nil
}

// fakeTurnsHarness is a minimal harness.Harness wrapping a fakeTurnsDriver,
// registered under a unique test-only name so it never collides with the
// real claude/codex/opencode adapters.
type fakeTurnsHarness struct {
	name   string
	driver *fakeTurnsDriver
}

func (h fakeTurnsHarness) Name() string                              { return h.name }
func (h fakeTurnsHarness) Binary() string                            { return h.name }
func (h fakeTurnsHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (h fakeTurnsHarness) SessionArgs(harness.SessionState) []string { return nil }
func (h fakeTurnsHarness) ValidateModel(string) error                { return nil }
func (h fakeTurnsHarness) DecodeOptions(map[string]any) (any, error) { return nil, nil }
func (h fakeTurnsHarness) OptionsSchema() []harness.OptionField      { return nil }
func (h fakeTurnsHarness) SupportsChannels() bool                    { return false }
func (h fakeTurnsHarness) ParseEvents(io.Reader) (harness.Result, error) {
	return harness.Result{}, nil
}
func (h fakeTurnsHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (h fakeTurnsHarness) SupportsKind(harness.Kind) bool                    { return true }
func (h fakeTurnsHarness) Driver() harness.SessionDriver                     { return h.driver }

const fakeTurnsHarnessName = "faketurns-webtest"

var registerFakeTurnsHarnessOnce sync.Once
var fakeTurnsDriverInstance = &fakeTurnsDriver{}

// registerFakeTurnsHarness registers fakeTurnsHarnessName once (the harness
// registry panics on duplicate registration) and returns the shared driver so
// each test can reset/inspect its recorded calls.
func registerFakeTurnsHarness() *fakeTurnsDriver {
	registerFakeTurnsHarnessOnce.Do(func() {
		harness.Register(fakeTurnsHarness{name: fakeTurnsHarnessName, driver: fakeTurnsDriverInstance})
	})
	return fakeTurnsDriverInstance
}

// TestWebAgentMessageDispatchesNonClaudeThroughDriver verifies that a message
// to a target resolving to a non-claude harness is delivered via
// driver.Inject with the resolved SessionHandle, and never touches tmux (the
// execCommand seam sees zero calls).
func TestWebAgentMessageDispatchesNonClaudeThroughDriver(t *testing.T) {
	drv := registerFakeTurnsHarness()
	drv.mu.Lock()
	drv.injects = nil
	drv.result = &harness.Result{Text: "turn done", SessionID: "thread-1"}
	drv.err = nil
	drv.mu.Unlock()

	s, _, svc := newTestServerWithAgents(t)
	wantHandle := harness.SessionHandle{
		Kind:        harness.KindAgent,
		Name:        "codex-worker",
		TmuxSession: agent.SessionName("codex-worker"),
		Workspace:   "/tmp/codex-worker",
	}
	svc.handles = map[string]resolvedHandle{
		"codex-worker": {harnessName: fakeTurnsHarnessName, handle: wantHandle},
	}

	var execCalls int
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		execCalls++
		return exec.Command("true")
	}

	reqBody := strings.NewReader(`{"text":"hello codex"}`)
	req := httptest.NewRequest("POST", "/web/agent/codex-worker/message", reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if execCalls != 0 {
		t.Fatalf("expected zero tmux exec calls for a non-claude target, got %d", execCalls)
	}

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if len(drv.injects) != 1 {
		t.Fatalf("expected exactly one Inject call, got %d", len(drv.injects))
	}
	got := drv.injects[0]
	if got.msg != "hello codex" {
		t.Errorf("Inject msg = %q, want %q", got.msg, "hello codex")
	}
	if got.handle.Name != wantHandle.Name || got.handle.TmuxSession != wantHandle.TmuxSession || got.handle.Workspace != wantHandle.Workspace {
		t.Errorf("Inject handle = %+v, want %+v", got.handle, wantHandle)
	}
}
