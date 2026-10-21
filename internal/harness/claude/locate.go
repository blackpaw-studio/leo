package claude

import (
	"errors"
	"os/exec"
)

// ErrNotFound is returned when the claude CLI is not installed (or not on the
// PATH leo runs under). It is not fatal to the daemon: only work that needs
// claude fails, and it starts working once the CLI is installed.
var ErrNotFound = errors.New("claude CLI not found; install it from https://claude.com/claude-code")

// Locate resolves the claude binary through lookPath (exec.LookPath when nil).
// A failed lookup is ErrNotFound.
func Locate(lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(Claude{}.Binary())
	if err != nil {
		return "", ErrNotFound
	}
	return path, nil
}

// LocateClaude is Locate through exec.LookPath, for use as a lookup seam.
func LocateClaude() (string, error) { return Locate(nil) }
