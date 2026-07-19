package web

import (
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// resolvedPaneWebTest is the pane id most of these tests stub list-panes to
// return, standing in for what a real ResolvePane call would find.
const resolvedPaneWebTest = "%9"

// isListPanesCall reports whether a recorded execCommand call's args are a
// list-panes invocation (mirrors internal/tmux's isListPanes helper for the
// web package's own execCommand seam, which has no context parameter).
func isListPanesCall(args []string) bool {
	return len(args) >= 3 && args[2] == "list-panes"
}

// targetOf returns the "-t" value from a recorded tmux call, or "" if none.
func targetOf(args []string) string {
	for i, a := range args {
		if a == "-t" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestWebAgentMessageUsesResolvedPaneTarget proves the fast-path message
// delivery targets the concrete pane list-panes reports, not
// tmux.PaneTarget's active-pane selector — so a split session doesn't
// misdirect the leo_send_message MCP tool's primary injection path.
func TestWebAgentMessageUsesResolvedPaneTarget(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll := messageInputPoll
	messageInputPoll = time.Millisecond
	defer func() { messageInputPoll = oldPoll }()

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if isListPanesCall(args) {
			return exec.Command("printf", "%s", resolvedPaneWebTest+"\n")
		}
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ hello there")
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
	for _, c := range calls {
		if isListPanesCall(c) {
			continue
		}
		if got := targetOf(c); got != resolvedPaneWebTest {
			t.Fatalf("call %v targeted %q, want resolved pane %q", c, got, resolvedPaneWebTest)
		}
	}
}

// TestWebAgentMessageFallsBackToPaneTargetWhenResolveFails proves the
// fast-path message delivery stays best-effort: when list-panes can't be
// resolved, it falls back to tmux.PaneTarget's active-pane selector rather
// than erroring louder than before pane resolution existed.
func TestWebAgentMessageFallsBackToPaneTargetWhenResolveFails(t *testing.T) {
	s, _ := newTestServer(t)

	oldPoll := messageInputPoll
	messageInputPoll = time.Millisecond
	defer func() { messageInputPoll = oldPoll }()

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if isListPanesCall(args) {
			return exec.Command("false")
		}
		if argsContain(args, "capture-pane") {
			return exec.Command("echo", "❯ hello there")
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
	wantTarget := "=leo-assistant:"
	found := false
	for _, c := range calls {
		if isListPanesCall(c) {
			continue
		}
		found = true
		if got := targetOf(c); got != wantTarget {
			t.Fatalf("call %v targeted %q, want fallback %q", c, got, wantTarget)
		}
	}
	if !found {
		t.Fatal("expected at least one non-list-panes tmux call")
	}
}

// TestWebAgentSendKeysUsesResolvedPaneTarget proves handleWebAgentSendKeys —
// both the char-split path (multi-char literals) and the single-key path —
// targets the concrete pane list-panes reports.
func TestWebAgentSendKeysUsesResolvedPaneTarget(t *testing.T) {
	s, _ := newTestServer(t)

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if isListPanesCall(args) {
			return exec.Command("printf", "%s", resolvedPaneWebTest+"\n")
		}
		return exec.Command("true")
	}

	body := strings.NewReader(`{"keys":["/clear","Enter"]}`)
	req := httptest.NewRequest("POST", "/web/agent/assistant/send", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	sawSendKeys := false
	for _, c := range calls {
		if isListPanesCall(c) {
			continue
		}
		sawSendKeys = true
		if got := targetOf(c); got != resolvedPaneWebTest {
			t.Fatalf("call %v targeted %q, want resolved pane %q", c, got, resolvedPaneWebTest)
		}
	}
	if !sawSendKeys {
		t.Fatal("expected at least one send-keys call")
	}
	// Resolved exactly once and reused for every send-keys call in the
	// request, not re-resolved per key.
	listPanesCalls := 0
	for _, c := range calls {
		if isListPanesCall(c) {
			listPanesCalls++
		}
	}
	if listPanesCalls != 1 {
		t.Fatalf("expected exactly 1 list-panes call (resolve once, reuse), got %d", listPanesCalls)
	}
}

// TestWebAgentInterruptUsesResolvedPaneTarget proves handleWebAgentInterrupt's
// immediate Escape burst (the 3 synchronous sends at request entry) targets
// the concrete pane list-panes reports, resolving once and reusing it rather
// than re-resolving per Escape. The background delayed burst is covered
// separately by TestWebAgentInterruptDelayedSendsReResolvePane, since it must
// NOT share this resolve-once posture — see that test.
func TestWebAgentInterruptUsesResolvedPaneTarget(t *testing.T) {
	s, _ := newTestServer(t)

	// Disable the background delayed burst — this test only covers the 3
	// immediate synchronous sends. Waiting for afterInterruptBurst (rather
	// than just not caring) guarantees no goroutine survives past this test
	// to race the next test's use of the shared interrupt-burst timing vars.
	oldAttempts := interruptDelayedAttempts
	interruptDelayedAttempts = 0
	defer func() { interruptDelayedAttempts = oldAttempts }()
	done := make(chan struct{})
	s.afterInterruptBurst = func() { close(done) }

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if isListPanesCall(args) {
			return exec.Command("printf", "%s", resolvedPaneWebTest+"\n")
		}
		return exec.Command("true")
	}

	req := httptest.NewRequest("POST", "/web/agent/assistant/interrupt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background delayed-burst goroutine never signaled completion")
	}

	listPanesCalls := 0
	for _, c := range calls {
		if isListPanesCall(c) {
			listPanesCalls++
			continue
		}
		if got := targetOf(c); got != resolvedPaneWebTest {
			t.Fatalf("call %v targeted %q, want resolved pane %q", c, got, resolvedPaneWebTest)
		}
	}
	if listPanesCalls != 1 {
		t.Fatalf("expected exactly 1 list-panes call (resolve once, reuse) for the immediate burst, got %d", listPanesCalls)
	}
}

// TestWebAgentInterruptDelayedSendsReResolvePane proves the background
// delayed-Escape burst re-resolves the pane before each send instead of
// reusing the request-entry resolution — the fix for a crash-restart racing
// an interrupt: if the tmux session is torn down and recreated during the
// ~2.5s delayed-burst window, a baked-in pane ID would silently no-op for the
// rest of the burst, where the old symbolic "=session:" target would have
// tracked the live session.
func TestWebAgentInterruptDelayedSendsReResolvePane(t *testing.T) {
	s, _ := newTestServer(t)

	oldAttempts, oldPoll := interruptDelayedAttempts, interruptDelayedPoll
	interruptDelayedAttempts = 1
	interruptDelayedPoll = time.Millisecond
	defer func() { interruptDelayedAttempts, interruptDelayedPoll = oldAttempts, oldPoll }()
	done := make(chan struct{})
	s.afterInterruptBurst = func() { close(done) }

	const initialPane = "%5"
	const laterPane = "%9"

	var mu sync.Mutex
	var calls [][]string
	listPanesCalls := 0
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, args)
		if isListPanesCall(args) {
			listPanesCalls++
			if listPanesCalls == 1 {
				// The immediate (request-entry) resolution.
				return exec.Command("printf", "%s", initialPane+"\n")
			}
			// The session was torn down and recreated — the delayed
			// goroutine's re-resolve must pick up the new pane.
			return exec.Command("printf", "%s", laterPane+"\n")
		}
		return exec.Command("true")
	}

	req := httptest.NewRequest("POST", "/web/agent/assistant/interrupt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background delayed-burst goroutine never signaled completion")
	}

	mu.Lock()
	defer mu.Unlock()
	if listPanesCalls < 2 {
		t.Fatalf("expected the delayed goroutine to re-resolve the pane, got %d list-panes calls", listPanesCalls)
	}
	sawDelayedEscape := false
	sawImmediateAtInitialPane := false
	for _, c := range calls {
		if isListPanesCall(c) {
			continue
		}
		switch targetOf(c) {
		case laterPane:
			sawDelayedEscape = true
		case initialPane:
			sawImmediateAtInitialPane = true
		}
	}
	if !sawDelayedEscape {
		t.Fatalf("expected a delayed Escape targeting the re-resolved pane %q, got calls=%v", laterPane, calls)
	}
	if !sawImmediateAtInitialPane {
		t.Fatalf("expected the immediate burst to still target the request-entry pane %q, got calls=%v", initialPane, calls)
	}
}
