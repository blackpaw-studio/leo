package prereq

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ClaudeResult holds the result of checking for the claude CLI.
type ClaudeResult struct {
	Path    string
	Version string
	OK      bool
}

// CheckClaude checks if the claude CLI is installed and reachable.
func CheckClaude() ClaudeResult {
	path, err := exec.LookPath("claude")
	if err != nil {
		return ClaudeResult{}
	}

	cmd := exec.Command(path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return ClaudeResult{Path: path, OK: true}
	}

	version := strings.TrimSpace(string(output))
	return ClaudeResult{Path: path, Version: version, OK: true}
}

// FindOpenClaw searches for an OpenClaw installation in common locations.
func FindOpenClaw() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".openclaw"),
	}

	entries, _ := os.ReadDir("/Volumes")
	for _, e := range entries {
		candidates = append(candidates, filepath.Join("/Volumes", e.Name(), ".openclaw"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	return ""
}

// FindExistingWorkspaces scans common locations for existing leo.yaml files.
func FindExistingWorkspaces() []string {
	home, _ := os.UserHomeDir()
	var found []string

	// Check direct children of home directory
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

	// Check /Volumes
	volumes, _ := os.ReadDir("/Volumes")
	for _, v := range volumes {
		vPath := filepath.Join("/Volumes", v.Name())
		vEntries, err := os.ReadDir(vPath)
		if err != nil {
			continue
		}
		for _, e := range vEntries {
			if !e.IsDir() {
				continue
			}
			candidate := filepath.Join(vPath, e.Name(), "leo.yaml")
			if _, err := os.Stat(candidate); err == nil {
				found = append(found, filepath.Join(vPath, e.Name()))
			}
		}
	}

	return found
}
