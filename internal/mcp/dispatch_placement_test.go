package mcp

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestFormatDispatchPlacementSplit(t *testing.T) {
	got := formatDispatchPlacement(consult.Started{Placement: "split", Pane: "%5", Window: "leo-ci-sign·5b4f"})
	want := " · pane %5 (title leo-ci-sign·5b4f)"
	if got != want {
		t.Fatalf("formatDispatchPlacement(split) = %q, want %q", got, want)
	}
}

func TestFormatDispatchPlacementWindow(t *testing.T) {
	got := formatDispatchPlacement(consult.Started{Placement: "window", Pane: "%9", Window: "paste-trailer·5b4f"})
	want := " · window paste-trailer·5b4f (pane %9)"
	if got != want {
		t.Fatalf("formatDispatchPlacement(window) = %q, want %q", got, want)
	}
}

func TestFormatDispatchPlacementHeadless(t *testing.T) {
	if got := formatDispatchPlacement(consult.Started{}); got != "" {
		t.Fatalf("formatDispatchPlacement(headless) = %q, want empty", got)
	}
}
