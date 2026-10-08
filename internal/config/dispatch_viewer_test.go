package config

import (
	"strings"
	"testing"
)

func TestDispatchViewerConfig(t *testing.T) {
	cfg := &Config{}
	if cfg.DispatchViewerPlacement() != "pane" || cfg.DispatchViewerMaxPanes() != 3 || cfg.DispatchViewerMainPaneHeight() != 60 {
		t.Fatal("wrong defaults")
	}
	zero := 0
	cfg.Defaults.Dispatch.Viewer.MaxPanes = &zero
	if cfg.Validate() == nil {
		t.Fatal("explicit zero accepted")
	}
	six, ninety := 6, 90
	cfg.Defaults.Dispatch.Viewer.MaxPanes, cfg.Defaults.Dispatch.Viewer.MainPaneHeight = &six, &ninety
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchViewerPlacementAcceptsBackground(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Dispatch.Viewer.Placement = "background"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("background rejected: %v", err)
	}
	if cfg.DispatchViewerPlacement() != "background" {
		t.Fatalf("placement=%q", cfg.DispatchViewerPlacement())
	}
	cfg.Defaults.Dispatch.Viewer.Placement = "floating"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must be pane, window or background") {
		t.Fatalf("err=%v", err)
	}
}
