package web

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
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
