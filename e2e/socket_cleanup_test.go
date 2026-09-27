//go:build e2e

package e2e

import "testing"

// These exercise the pure path-resolution logic behind killTmuxServer without
// spawning tmux, so they're safe to run directly (not just compile-check)
// even though they live under the e2e build tag.

func TestTmuxSocketPathDefaultsToTmp(t *testing.T) {
	got := tmuxSocketPath("", 501, "leo-e2e-123")
	want := "/tmp/tmux-501/leo-e2e-123"
	if got != want {
		t.Fatalf("tmuxSocketPath = %q, want %q", got, want)
	}
}

func TestTmuxSocketPathUsesTmuxTmpdir(t *testing.T) {
	got := tmuxSocketPath("/custom/dir", 501, "leo-e2e-123")
	want := "/custom/dir/tmux-501/leo-e2e-123"
	if got != want {
		t.Fatalf("tmuxSocketPath = %q, want %q", got, want)
	}
}
