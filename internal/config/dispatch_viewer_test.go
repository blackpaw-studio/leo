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

// loadViewerCfg loads yaml with the managed probe stubbed to managed.
func loadViewerCfg(t *testing.T, managed bool, yaml string) *Config {
	t.Helper()
	t.Cleanup(SetManagedProbe(func(string) bool { return managed }))
	cfg, err := Load(writeFile(t, t.TempDir(), "leo.yaml", yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDispatchViewerPlacementManagedDefault(t *testing.T) {
	cases := []struct {
		name    string
		managed bool
		yaml    string
		want    string
	}{
		{"managed unset defaults to window", true, "defaults: {model: sonnet}\n", "window"},
		{"managed explicit pane wins", true, "defaults:\n  dispatch: {viewer: {placement: pane}}\n", "pane"},
		{"managed explicit background wins", true, "defaults:\n  dispatch: {viewer: {placement: background}}\n", "background"},
		{"unmanaged unset keeps pane", false, "defaults: {model: sonnet}\n", "pane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadViewerCfg(t, tc.managed, tc.yaml).DispatchViewerPlacement(); got != tc.want {
				t.Fatalf("placement = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDispatchViewerPlacementManagedFollowerTakesLead(t *testing.T) {
	allowProbes(t)
	t.Cleanup(SetManagedProbe(func(string) bool { return true }))
	dir := t.TempDir()
	writeLeadCache(t, dir, "defaults:\n  dispatch: {viewer: {placement: background}}\n")
	cfg, err := Load(writeFile(t, dir, "leo.yaml", followerHeader))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.DispatchViewerPlacement(); got != "background" {
		t.Fatalf("placement = %q, want the lead's background", got)
	}
}
