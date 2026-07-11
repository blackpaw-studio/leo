package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeTurnDriver is a minimal harness.SessionDriver stub for exercising the
// DriveTurns branch of superviseProcess without a real turn-based harness.
type fakeTurnDriver struct {
	style      harness.DriveStyle
	startCalls atomic.Int32
	startErr   error
}

func (f *fakeTurnDriver) Style() harness.DriveStyle { return f.style }

func (f *fakeTurnDriver) Start(_ context.Context, _ harness.SessionHandle) error {
	f.startCalls.Add(1)
	return f.startErr
}

func (f *fakeTurnDriver) Inject(_ context.Context, _ harness.SessionHandle, _ string) (*harness.Result, error) {
	return nil, nil
}

func (f *fakeTurnDriver) Attach(_ harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{}, nil
}

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

func TestSuperviseTurnBasedRunsStartAndBlocksUntilCancel(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.initState("turnproc")
	home := t.TempDir()
	id := newProcIdentity("turnproc", nil)
	spec := ProcessSpec{Name: "turnproc", Kind: harness.KindProcess}
	drv := &fakeTurnDriver{style: harness.DriveTurns}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		superviseTurnBased(ctx, spec, home, sv, id, drv)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for drv.startCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("driver Start was never called")
		case <-time.After(5 * time.Millisecond):
		}
	}

	sv.mu.RLock()
	status := sv.states["turnproc"].Status
	sv.mu.RUnlock()
	if status != "running" {
		t.Errorf("status = %q, want running", status)
	}

	// No restart loop, no resident tmux session: the goroutine must not
	// return on its own — it blocks until ctx is cancelled.
	select {
	case <-done:
		t.Fatal("superviseTurnBased returned before ctx was cancelled")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("superviseTurnBased did not exit on cancel")
	}

	sv.mu.RLock()
	status = sv.states["turnproc"].Status
	sv.mu.RUnlock()
	if status != "stopped" {
		t.Errorf("status after cancel = %q, want stopped", status)
	}
	if drv.startCalls.Load() != 1 {
		t.Errorf("driver Start called %d times, want exactly 1 (no restart loop)", drv.startCalls.Load())
	}
}

func TestSuperviseTurnBasedMarksStoppedOnStartError(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.initState("turnproc2")
	home := t.TempDir()
	id := newProcIdentity("turnproc2", nil)
	spec := ProcessSpec{Name: "turnproc2"}
	drv := &fakeTurnDriver{style: harness.DriveTurns, startErr: context.DeadlineExceeded}

	superviseTurnBased(context.Background(), spec, home, sv, id, drv)

	sv.mu.RLock()
	status := sv.states["turnproc2"].Status
	sv.mu.RUnlock()
	if status != "stopped" {
		t.Errorf("status = %q, want stopped", status)
	}
}
