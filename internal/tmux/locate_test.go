package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocateUsesPathFirst(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir)
	// Defeat any cached PATH in the test binary by forcing a fresh lookup.
	got, err := Locate()
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != fake {
		t.Fatalf("Locate = %q, want %q", got, fake)
	}
}

func TestLocateFallsBackToKnownPaths(t *testing.T) {
	// Point PATH at an empty directory so LookPath fails.
	t.Setenv("PATH", t.TempDir())

	// Swap fallbackPaths to a synthetic binary we control so the test works
	// regardless of what's installed on the runner.
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	old := fallbackPaths
	fallbackPaths = []string{fake}
	t.Cleanup(func() { fallbackPaths = old })

	got, err := Locate()
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != fake {
		t.Fatalf("Locate = %q, want %q", got, fake)
	}
}

func TestLocateReturnsErrNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	old := fallbackPaths
	fallbackPaths = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	t.Cleanup(func() { fallbackPaths = old })

	_, err := Locate()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Locate err = %v, want ErrNotFound", err)
	}
}
