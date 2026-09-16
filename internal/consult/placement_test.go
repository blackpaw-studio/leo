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
