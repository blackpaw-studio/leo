package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestDriverForEmptyDefaultsToClaude(t *testing.T) {
	drv := driverFor("")
	if drv == nil {
		t.Fatal("expected a non-nil driver for empty harness (defaults to claude)")
	}
	if drv.Style() != harness.DriveTmux {
		t.Errorf("Style() = %q, want %q", drv.Style(), harness.DriveTmux)
	}
}

func TestDriverForClaudeExplicit(t *testing.T) {
	drv := driverFor("claude")
	if drv == nil || drv.Style() != harness.DriveTmux {
		t.Fatalf("driverFor(claude) = %#v, want a DriveTmux driver", drv)
	}
}

func TestDriverForUnknownHarnessReturnsNil(t *testing.T) {
	if drv := driverFor("does-not-exist"); drv != nil {
		t.Errorf("expected nil driver for an unknown harness, got %T", drv)
	}
}

// TestClaudeDriverStartIsNoOp locks in the "zero new tmux/exec calls"
// characterization for existing (claude) configs: driverFor("").Start must
// return immediately with no error and no side effects, so wiring the
// DriveTmux Start call into superviseProcess is behavior-neutral.
func TestClaudeDriverStartIsNoOp(t *testing.T) {
	drv := driverFor("")
	done := make(chan error, 1)
	go func() { done <- drv.Start(context.Background(), harness.SessionHandle{Name: "x"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("claude driver Start() = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("claude driver Start() did not return promptly (expected a no-op)")
	}
}

func TestHandleForSpecDefaultsKindToProcess(t *testing.T) {
	home := t.TempDir()
	id := newProcIdentity("myproc", []string{"--model", "sonnet"})
	spec := ProcessSpec{Name: "myproc", WorkDir: "/tmp/ws", Env: map[string]string{"A": "1"}, OpeningPrompt: "hi"}

	h := handleForSpec(spec, id, home)
	if h.Kind != harness.KindProcess {
		t.Errorf("Kind = %q, want %q", h.Kind, harness.KindProcess)
	}
	if h.Name != "myproc" {
		t.Errorf("Name = %q, want myproc", h.Name)
	}
	if h.TmuxSession != "leo-myproc" {
		t.Errorf("TmuxSession = %q, want leo-myproc", h.TmuxSession)
	}
	if h.OpeningPrompt != "hi" {
		t.Errorf("OpeningPrompt = %q, want hi", h.OpeningPrompt)
	}
	if h.HomePath != home {
		t.Errorf("HomePath = %q, want %q", h.HomePath, home)
	}
	if h.IDs == nil {
		t.Error("expected a non-nil IDs store")
	}
}

func TestHandleForSpecPreservesExplicitKind(t *testing.T) {
	home := t.TempDir()
	id := newProcIdentity("agent1", nil)
	spec := ProcessSpec{Name: "agent1", Kind: harness.KindAgent}

	h := handleForSpec(spec, id, home)
	if h.Kind != harness.KindAgent {
		t.Errorf("Kind = %q, want %q", h.Kind, harness.KindAgent)
	}
}
