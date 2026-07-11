package prereq

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	lookPath   = exec.LookPath
	runCommand = func(path string, args ...string) ([]byte, error) {
		return exec.Command(path, args...).Output()
	}
	userHomeDir = os.UserHomeDir
)

// BinaryResult holds the result of checking for a harness CLI binary.
type BinaryResult struct {
	Path    string
	Version string
	OK      bool
}

// CheckBinary checks if the named CLI binary is installed and reachable.
func CheckBinary(name string) BinaryResult {
	path, err := lookPath(name)
	if err != nil {
		return BinaryResult{}
	}

	output, err := runCommand(path, "--version")
	if err != nil {
		return BinaryResult{Path: path, OK: true}
	}

	version := strings.TrimSpace(string(output))
	return BinaryResult{Path: path, Version: version, OK: true}
}

// CheckTmux checks if tmux is installed and reachable.
func CheckTmux() bool {
	for _, p := range []string{"tmux", "/opt/homebrew/bin/tmux", "/usr/local/bin/tmux"} {
		if path, err := lookPath(p); err == nil && path != "" {
			return true
		}
	}
	return false
}

// FindOpenClaw searches for an OpenClaw installation in common locations.
func FindOpenClaw() string {
	home, _ := userHomeDir()

	ocPath := filepath.Join(home, ".openclaw")
	if _, err := os.Stat(ocPath); err == nil {
		return ocPath
	}

	return ""
}
