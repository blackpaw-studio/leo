package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
)

// A daemon with the web UI off still serves the bridge, and a mod waits for a
// state line before it trusts the policy (else it warns "no state from leo").
// The web server owns the dispatch-aware pusher, so StartWeb must fall back to
// a dispatch-less one when web is disabled.
func TestStartWebDisabledStillPushesBridgeState(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("defaults:\n  model: sonnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub := bridge.New(bridge.Options{})
	s := New(tmpSockPath(t, "b.sock"), cfgPath, nil, WithBridge(hub))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.SetParentContext(ctx)
	s.bridgeStateInterval = 10 * time.Millisecond

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartWeb(cfg, nil); err != nil {
		t.Fatalf("StartWeb: %v", err)
	}
	if _, err := hub.Open("alpha", "launch-1"); err != nil {
		t.Fatal(err)
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
		t.Fatalf("no state pushed to a connected mod: %v", err)
	}
	var got struct {
		Op         string                 `json:"op"`
		Delegation bridge.DelegationState `json:"delegation"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	// No dispatcher runs without the web server, so delegation stays off:
	// native agents are not hidden behind tools that cannot work.
	if got.Op != bridge.OpState || got.Delegation.Enabled {
		t.Fatalf("pushed %s, want a state line with delegation off", line)
	}
}
