package web

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestSetupConsultRuntimeUsesContextExecSeamForViewer(t *testing.T) {
	var contextCalls atomic.Int32
	s := &Server{
		configPath: "test.yaml",
		leoPath:    "leo",
		execCommand: func(string, ...string) *exec.Cmd {
			t.Fatal("viewer used legacy command seam while context seam was configured")
			return nil
		},
		execCommandContext: func(_ context.Context, _ string, args ...string) *exec.Cmd {
			contextCalls.Add(1)
			for _, arg := range args {
				if arg == "new-window" {
					return exec.Command("printf", "@7\n")
				}
			}
			return exec.Command("true")
		},
	}
	s.setupConsultRuntime(Options{}, func(string) (string, bool) { return "leo-worker", true })
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", `{"type":"result","result":"done","is_error":false}`)
	}
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{"worker": {Harness: "claude", Model: "sonnet"}}}
	if _, err := s.consults.Start(context.Background(), cfg, consult.Request{Caller: "caller", Template: "worker", Prompt: "go", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if contextCalls.Load() == 0 {
		t.Fatal("context-aware viewer seam was not called")
	}
}

func TestShutdownStopsConsultRuntimeLoop(t *testing.T) {
	var rosterCalls atomic.Int32
	s := &Server{
		configPath:         "test.yaml",
		leoPath:            "leo",
		execCommand:        func(string, ...string) *exec.Cmd { return exec.Command("true") },
		execCommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "true") },
		consultIntervals: consultLoopIntervals{
			sweep:      time.Millisecond,
			roster:     time.Millisecond,
			rosterIdle: time.Millisecond,
			viewer:     time.Millisecond,
		},
		updateRoster: func([]consult.Record, time.Time) { rosterCalls.Add(1) },
	}
	// A never-canceled parent: Shutdown alone must stop the loop.
	s.setupConsultRuntime(Options{ParentContext: context.Background()}, func(string) (string, bool) { return "", false })

	deadline := time.Now().Add(2 * time.Second)
	for rosterCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("roster loop never ran")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	after := rosterCalls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := rosterCalls.Load(); got != after {
		t.Fatalf("roster updated %d times after Shutdown", got-after)
	}
}
