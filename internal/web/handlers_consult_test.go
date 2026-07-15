package web

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestDeliverConsultReplyLiveAgent(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	var gotSession, gotBody string
	s.injectPrompt = func(_ context.Context, session, body string) error {
		gotSession, gotBody = session, body
		return nil
	}
	// "assistant" is live in mockProcesses.states.
	if err := s.deliverConsultReply(context.Background(), "assistant", "[consult c-1] hi"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if gotSession != agent.SessionName("assistant") || gotBody != "[consult c-1] hi" {
		t.Fatalf("injected (%q, %q)", gotSession, gotBody)
	}
}

func TestDeliverConsultReplyResumesSuspendedCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	var gotSession string
	s.injectPrompt = func(_ context.Context, session, _ string) error {
		gotSession = session
		return nil
	}
	// "leo-coding-leo" exists as a record but is NOT in processes.States()
	// → delivery must resume it first, then inject.
	if err := s.deliverConsultReply(context.Background(), "leo-coding-leo", "body"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if !svc.resumeCalled || svc.resumeName != "leo-coding-leo" {
		t.Fatalf("expected resume of caller, got %+v", svc)
	}
	if gotSession != agent.SessionName("leo-coding-leo") {
		t.Fatalf("injected into %q", gotSession)
	}
}

func TestDeliverConsultReplyUnknownCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.resumeErr = &agent.ErrNotFound{Query: "ghost"}
	if err := s.deliverConsultReply(context.Background(), "ghost", "body"); err == nil {
		t.Fatal("expected error for unknown caller")
	}
}

// TestDeliverConsultReplyNonClaudeDriver verifies that a consult reply
// destined for a caller resolving to a non-claude harness is delivered via
// driver.Inject (bypassing the tmux/readiness-probing injectPrompt path
// entirely).
func TestDeliverConsultReplyNonClaudeDriver(t *testing.T) {
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

	s.injectPrompt = func(context.Context, string, string) error {
		t.Fatal("injectPrompt should not be called for a non-claude caller")
		return nil
	}

	if err := s.deliverConsultReply(context.Background(), "codex-worker", "[consult c-1] hi"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if len(drv.injects) != 1 {
		t.Fatalf("expected exactly one Inject call, got %d", len(drv.injects))
	}
	got := drv.injects[0]
	if got.msg != "[consult c-1] hi" {
		t.Errorf("Inject msg = %q, want %q", got.msg, "[consult c-1] hi")
	}
	if got.handle.Name != wantHandle.Name || got.handle.TmuxSession != wantHandle.TmuxSession || got.handle.Workspace != wantHandle.Workspace {
		t.Errorf("Inject handle = %+v, want %+v", got.handle, wantHandle)
	}
}
