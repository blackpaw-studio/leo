package consult

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestRuntimeViewerOverridesBoundsHungTmux(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.Timeout = 50 * time.Millisecond
	r.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	done := make(chan ViewerOverrides, 1)
	go func() { done <- r.ViewerOverrides(context.Background(), "leo-orch") }()
	select {
	case got := <-done:
		if got != (ViewerOverrides{}) {
			t.Fatalf("overrides = %+v, want none from a hung tmux", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ViewerOverrides blocked on a hung tmux show-options")
	}
}
