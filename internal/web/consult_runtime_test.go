package web

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

func TestSetupConsultRuntimeUsesContextExecSeamForViewer(t *testing.T) {
	var contextCalls atomic.Int32
	s := &Server{
		ConfigService: &ConfigService{
			configPath: "test.yaml",
			leoPath:    "leo",
			execCommand: func(string, ...string) *exec.Cmd {
				t.Fatal("viewer used legacy command seam while context seam was configured")
				return nil
			},
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
		ConfigService:      &ConfigService{configPath: "test.yaml", leoPath: "leo", execCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }},
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

func TestConsultRuntimeLoopPollsLivePlacementOnEachTickUntilShutdown(t *testing.T) {
	ticks := make(chan time.Time)
	polled := make(chan struct{})
	s := &Server{
		ConfigService:      &ConfigService{configPath: "test.yaml", leoPath: "leo", execCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }},
		execCommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "true") },
		updateRoster:       func([]consult.Record, time.Time) {},
		placementTicks:     ticks,
		pollPlacement: func(ctx context.Context) {
			select {
			case polled <- struct{}{}:
			case <-ctx.Done():
			}
		},
	}
	s.setupConsultRuntime(Options{ParentContext: context.Background()}, func(string) (string, bool) { return "", false })

	for range 3 {
		ticks <- time.Time{}
		<-polled
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Shutdown waits for the loop to exit, so nobody is left to take a tick.
	select {
	case ticks <- time.Time{}:
		t.Fatal("placement loop still running after Shutdown")
	default:
	}
}

// A dispatch whose pane survives a daemon restart is adopted before the
// daemon settles which bridge keys it will adopt: the claude still running
// there reconnects under the launch its pane was tagged with, and its
// reports finish the turn it was in.
func TestSetupConsultRuntimeReopensAnAdoptedDispatchsBridge(t *testing.T) {
	const id, launch = "d-web", "tok_ADOPT"
	stateDir := t.TempDir()
	recorder := consult.NewFileRecorder(stateDir)
	h, err := recorder.Open(consult.Record{ID: id, Kind: "dispatch", Template: "worker", Harness: "claude", Mode: consult.ModeInteractive,
		Status: consult.StatusRunning, PaneID: "%9", ViewerKind: "window", StartedAt: time.Now().Add(-time.Minute),
		Turns: []consult.Turn{{TurnID: id + "#1", Source: consult.TurnSourceOrchestrator, Delivered: true, SlotHeld: true, HarnessTurnID: "t1"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(consult.StatusRunning, nil)
	startCommand := "exec 'env' 'LEO_DISPATCH_ID=" + id + "' 'claude'"
	s := &Server{
		ConfigService: &ConfigService{configPath: "test.yaml", leoPath: "leo", execCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }},
		execCommandContext: func(_ context.Context, _ string, args ...string) *exec.Cmd {
			if slices.Contains(args, "list-panes") && slices.Contains(args, "%9") {
				return exec.Command("printf", "%s\n", "%9\t0\t"+launch+"\t"+startCommand)
			}
			return exec.Command("true")
		},
	}
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{StateDir: t.TempDir(), LeoVersion: "vtest", LeoBin: "/opt/leo", Log: io.Discard})
	s.setupConsultRuntime(Options{ConsultRecorder: recorder, Bridge: BridgeOptions{Router: &bridge.Router{Hub: hub}, Launcher: launcher}}, nil)
	hub.AwaitAdoption(nil) // as RestoreAgents does, once the agents are restored

	key := consult.DispatchBridgeKey(id)
	if _, err := hub.Connect(key, launch); err != nil {
		t.Fatalf("the adopted dispatch's claude cannot reconnect: %v", err)
	}
	for _, r := range []bridge.Report{
		{Type: bridge.ReportHello, SessionID: "s1", ClaudeVersion: "2.1.289"},
		{Type: bridge.ReportEvent, Name: bridge.EventTurnComplete, TurnID: "t1", Message: "ADOPT-OK", EventID: "turn.complete:t1"},
	} {
		if err := hub.Apply(key, launch, r); err != nil {
			t.Fatalf("applying %s: %v", r.Type, err)
		}
	}
	rec, err := s.consults.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != consult.StatusIdle || rec.Turns[0].Outcome != consult.TurnFinished || rec.Turns[0].Text != "ADOPT-OK" {
		t.Fatalf("status=%s turn=%+v; want the adopted turn finished with its text", rec.Status, rec.Turns[0])
	}
}

// The interactive dispatch runtime persists this path into agent hook files:
// it must follow the daemon's resolved leo binary (a managed install's stable
// self_path), falling back to the running executable, then to the bare path.
func TestInteractiveLeoPath(t *testing.T) {
	running := func() (string, error) { return "/running/leo", nil }
	failing := func() (string, error) { return "", errors.New("no exe") }
	tests := []struct {
		name     string
		bin      string
		exe      func() (string, error)
		fallback string
		want     string
	}{
		{"daemon binary wins", "/app/Support/bin/leo", running, "leo", "/app/Support/bin/leo"},
		{"running executable when daemon binary unknown", "", running, "leo", "/running/leo"},
		{"absolute fallback when executable fails", "", failing, "/fallback/leo", "/fallback/leo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := interactiveLeoPath(tc.bin, tc.exe, tc.fallback); got != tc.want {
				t.Fatalf("interactiveLeoPath = %q, want %q", got, tc.want)
			}
		})
	}
}
