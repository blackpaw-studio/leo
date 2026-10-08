package consult

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestResolveViewerPlacement(t *testing.T) {
	three, sixty := 3, 60
	cfg := &config.Config{Defaults: config.DefaultsConfig{Dispatch: config.DispatchConfig{Viewer: config.DispatchViewerConfig{MaxPanes: &three, MainPaneHeight: &sixty}}}}
	rec := Record{CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	if got := ResolveViewerPlacement(rec, ViewerOverrides{}, cfg, 2); got.Kind != "split" || got.Target != "%1" {
		t.Fatalf("placement=%+v", got)
	}
	if got := ResolveViewerPlacement(rec, ViewerOverrides{}, cfg, 3); got.Kind != "window" {
		t.Fatalf("placement=%+v", got)
	}
	if got := ResolveViewerPlacement(rec, ViewerOverrides{Placement: "window"}, cfg, 0); got.Kind != "window" {
		t.Fatalf("override=%+v", got)
	}
	if got := ResolveViewerPlacement(Record{}, ViewerOverrides{}, cfg, 0); got.Kind != "window" {
		t.Fatalf("fallback=%+v", got)
	}
}

func TestLiveViewerPaneCount(t *testing.T) {
	records := []Record{
		{CallerSessionID: "$1", CallerWindowID: "@1", ViewerKind: "split", ViewerPaneID: "%2", Status: StatusRunning},
		{CallerSessionID: "$1", CallerWindowID: "@1", ViewerKind: "split", ViewerPaneID: "%6", Status: StatusFailed},
		{CallerSessionID: "$1", CallerWindowID: "@1", ViewerKind: "split", PaneID: "%3", Mode: ModeInteractive, Status: StatusIdle},
		{CallerSessionID: "$1", CallerWindowID: "@1", ViewerKind: "split", PaneID: "%4", Mode: ModeInteractive, Status: StatusReleased},
		{CallerSessionID: "$1", CallerWindowID: "@2", ViewerKind: "split", ViewerPaneID: "%5", Status: StatusRunning},
	}
	if got := LiveViewerPaneCount(records, "$1", "@1"); got != 3 {
		t.Fatalf("count=%d", got)
	}
}

func TestResolveViewerPlacementBackground(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.Dispatch.Viewer.Placement = "background"
	inPane := Record{CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	for name, rec := range map[string]Record{
		"caller in a pane":              inPane,
		"no caller pane":                {},
		"nested in the dispatch window": {CallerPaneID: "%7", CallerSessionID: dispatchViewerSession, CallerWindowID: "@3"},
	} {
		got := ResolveViewerPlacement(rec, ViewerOverrides{}, cfg, 0)
		if got.Kind != "window" || !got.Background || got.Target != "" {
			t.Fatalf("%s: placement=%+v, want a background window", name, got)
		}
	}
	if got := ResolveViewerPlacement(inPane, ViewerOverrides{Placement: "pane"}, cfg, 0); got.Kind != "split" || got.Background {
		t.Fatalf("pane override=%+v", got)
	}
	if got := ResolveViewerPlacement(inPane, ViewerOverrides{Placement: "window"}, cfg, 0); got.Kind != "window" || got.Background {
		t.Fatalf("window override=%+v", got)
	}
	cfg.Defaults.Dispatch.Viewer.Placement = "pane"
	if got := ResolveViewerPlacement(inPane, ViewerOverrides{Placement: "background"}, cfg, 0); got.Kind != "window" || !got.Background {
		t.Fatalf("background override=%+v", got)
	}
	if got := ResolveViewerPlacement(Record{}, ViewerOverrides{Placement: "background"}, cfg, 0); !got.Background {
		t.Fatalf("background override without a caller pane=%+v", got)
	}
}
