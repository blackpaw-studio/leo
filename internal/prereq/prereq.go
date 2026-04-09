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

// ClaudeResult holds the result of checking for the claude CLI.
type ClaudeResult struct {
	Path    string
	Version string
	OK      bool
}

// CheckClaude checks if the claude CLI is installed and reachable.
func CheckClaude() ClaudeResult {
	path, err := lookPath("claude")
	if err != nil {
		return ClaudeResult{}
	}

	output, err := runCommand(path, "--version")
	if err != nil {
		return ClaudeResult{Path: path, OK: true}
	}

	version := strings.TrimSpace(string(output))
	return ClaudeResult{Path: path, Version: version, OK: true}
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

// CheckBun checks if bun is installed and executable.
func CheckBun() bool {
	home, _ := userHomeDir()
	candidates := []string{"bun", "/opt/homebrew/bin/bun", "/usr/local/bin/bun"}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".bun", "bin", "bun"))
	}
	for _, p := range candidates {
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

// FindExistingWorkspaces scans common locations for existing leo.yaml files.
func FindExistingWorkspaces() []string {
	home, _ := userHomeDir()
	var found []string

	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(home, e.Name(), "leo.yaml")
		if _, err := os.Stat(candidate); err == nil {
			found = append(found, filepath.Join(home, e.Name()))
		}
	}

	return found
}
