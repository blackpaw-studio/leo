package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

func bridgeDelegationConfig(enabled bool) *config.Config {
	cfg := &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles:         map[string]config.RoleSpec{"implement": {UseFor: "write code"}},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "t"}}}},
	}}
	cfg.Delegation.SetEnabled(enabled)
	return cfg
}

func TestDelegationStateFromConfig(t *testing.T) {
	on := delegationStateFromConfig(bridgeDelegationConfig(true))
	if !on.Enabled || on.Section == "" || len(on.HideAgents) != len(config.DefaultHiddenNativeAgents) {
		t.Fatalf("on = %+v", on)
	}
	off := delegationStateFromConfig(bridgeDelegationConfig(false))
	if off.Enabled || off.Section != "" {
		t.Fatalf("off = %+v", off)
	}
	if got := delegationStateFromConfig(&config.Config{}); got.Enabled {
		t.Fatalf("unconfigured = %+v", got)
	}
}

func TestDelegationSourceRereadsOnlyAChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	loads := 0
	enabled := true
	src := &delegationSource{path: path, load: func() (*config.Config, error) {
		loads++
		return bridgeDelegationConfig(enabled), nil
	}}
	first, second := src.Get(), src.Get()
	if !first.Enabled || !second.Enabled || loads != 1 {
		t.Fatalf("loads = %d, want 1 for an unchanged file", loads)
	}
	enabled = false
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if src.Get().Enabled || loads != 2 {
		t.Fatalf("a changed file was not reread (loads %d)", loads)
	}
	src.load = func() (*config.Config, error) { loads++; return nil, errors.New("broken yaml") }
	if err := os.Chtimes(path, later.Add(time.Minute), later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := src.Get(); got.Enabled || loads != 3 {
		t.Fatalf("a failed reload must keep the last state, got %+v (loads %d)", got, loads)
	}
}

func TestSetupConsultRuntimeServesBridgeRequestsAndPushesState(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	if err := config.Save(s.configPath, bridgeDelegationConfig(true)); err != nil {
		t.Fatal(err)
	}
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{StateDir: t.TempDir(), LeoVersion: "vtest", LeoBin: "/opt/leo", Log: io.Discard})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.consultIntervals = consultLoopIntervals{bridgeState: 10 * time.Millisecond}
	s.setupConsultRuntime(Options{ParentContext: ctx, Bridge: BridgeOptions{Router: &bridge.Router{Hub: hub}, Launcher: launcher}}, nil)
	t.Cleanup(func() {
		if s.stopConsultLoop != nil {
			s.stopConsultLoop()
		}
	})

	if _, err := hub.Open("alpha", "launch-1"); err != nil {
		t.Fatal(err)
	}
	req := bridge.Report{Type: bridge.ReportRequest, Op: bridge.RequestDispatchCancel, DispatchID: "nope"}
	if err := hub.Apply("alpha", "launch-1", req); !errors.Is(err, bridge.ErrRequestDenied) {
		t.Fatalf("request: err = %v, want the dispatcher's ErrRequestDenied", err)
	}

	stream, err := hub.Connect("alpha", "launch-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	lineCtx, lineCancel := context.WithTimeout(ctx, 5*time.Second)
	defer lineCancel()
	line, err := stream.NextLine(lineCtx)
	if err != nil {
		t.Fatalf("no state pushed: %v", err)
	}
	var got struct {
		Op         string                 `json:"op"`
		Delegation bridge.DelegationState `json:"delegation"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	if got.Op != bridge.OpState || !got.Delegation.Enabled || got.Delegation.Section == "" {
		t.Fatalf("pushed %s", line)
	}
}
