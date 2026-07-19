package web

import (
	"net/http/httptest"
	"os/exec"
	"strings"
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

// TestWebAgentInterruptUsesResolvedPaneTarget proves handleWebAgentInterrupt
// targets the concrete pane list-panes reports for its Escape burst,
// resolving once and reusing it rather than re-resolving per Escape.
func TestWebAgentInterruptUsesResolvedPaneTarget(t *testing.T) {
	s, _ := newTestServer(t)

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
	// Give the background goroutine's delayed escapes no time to run —
	// only the 3 immediate sends matter here (deterministic without sleeps).
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
		t.Fatalf("expected exactly 1 list-panes call (resolve once, reuse), got %d", listPanesCalls)
	}
}
