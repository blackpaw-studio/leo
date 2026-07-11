package claude

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestTmuxTUIDriverStyle(t *testing.T) {
	if got := (Claude{}).Driver().Style(); got != harness.DriveTmux {
		t.Fatalf("Style() = %q, want %q", got, harness.DriveTmux)
	}
}

func TestTmuxTUIDriverStartIsNoOp(t *testing.T) {
	if err := (Claude{}).Driver().Start(context.Background(), harness.SessionHandle{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestTmuxTUIDriverInjectDelegatesToInjectPrompt(t *testing.T) {
	var gotSession, gotBody string
	restore := SetInjectPromptForTest(func(ctx context.Context, tmuxPath, session, body string) error {
		gotSession, gotBody = session, body
		return nil
	})
	defer restore()
	res, err := (Claude{}).Driver().Inject(context.Background(), harness.SessionHandle{TmuxSession: "leo-x"}, "hello")
	if err != nil || res != nil {
		t.Fatalf("Inject = (%v, %v), want (nil, nil)", res, err)
	}
	if gotSession != "leo-x" || gotBody != "hello" {
		t.Fatalf("delegated (%q, %q)", gotSession, gotBody)
	}
}
