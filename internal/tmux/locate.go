// Package tmux locates the tmux binary in a way that tolerates stripped-down
// PATH environments. Several leo entry points (SSH non-interactive shells,
// the web UI spawned by launchd, the service supervisor under systemd) do not
// inherit the interactive shell's PATH, and homebrew paths like
// /opt/homebrew/bin typically live in .zprofile/.zshrc which are not sourced
// in those contexts. Checking a small set of well-known install locations
// after PATH keeps `leo attach` and friends working without requiring the
// user to configure an explicit path.
package tmux

import (
	"errors"
	"os"
	"os/exec"
)

// fallbackPaths is the ordered list of absolute paths probed when tmux is not
// on $PATH. Covers macOS arm64 homebrew, macOS intel homebrew, and the
// standard Linux distro location.
var fallbackPaths = []string{
	"/opt/homebrew/bin/tmux",
	"/usr/local/bin/tmux",
	"/usr/bin/tmux",
}

// ErrNotFound is returned when tmux cannot be located on PATH or in any of
// the well-known fallback directories.
var ErrNotFound = errors.New("tmux not found: install with 'brew install tmux' or set PATH to include tmux")

// Locate returns the absolute path to the tmux binary. It first consults
// $PATH via exec.LookPath, then falls back to a small set of well-known
// install locations. Returns ErrNotFound when no candidate exists.
func Locate() (string, error) {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p, nil
	}
	for _, p := range fallbackPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", ErrNotFound
}
