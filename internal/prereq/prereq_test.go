package prereq

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckClaudeFound(t *testing.T) {
	original := lookPath
	originalRun := runCommand
	defer func() {
		lookPath = original
		runCommand = originalRun
	}()

	lookPath = func(file string) (string, error) {
		return "/usr/local/bin/claude", nil
	}
	runCommand = func(path string, args ...string) ([]byte, error) {
		return []byte("claude 1.0.0\n"), nil
	}

	result := CheckClaude()

	if !result.OK {
		t.Error("expected OK = true")
	}
	if result.Path != "/usr/local/bin/claude" {
		t.Errorf("Path = %q, want %q", result.Path, "/usr/local/bin/claude")
	}
	if result.Version != "claude 1.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "claude 1.0.0")
	}
}

func TestCheckClaudeFoundNoVersion(t *testing.T) {
	original := lookPath
	originalRun := runCommand
	defer func() {
		lookPath = original
		runCommand = originalRun
	}()

	lookPath = func(file string) (string, error) {
		return "/usr/local/bin/claude", nil
	}
	runCommand = func(path string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("exit status 1")
	}

	result := CheckClaude()

	if !result.OK {
		t.Error("expected OK = true even without version")
	}
	if result.Version != "" {
		t.Errorf("Version = %q, want empty", result.Version)
	}
}

func TestCheckClaudeNotFound(t *testing.T) {
	original := lookPath
	defer func() { lookPath = original }()

	lookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	result := CheckClaude()

	if result.OK {
		t.Error("expected OK = false when not found")
	}
	if result.Path != "" {
		t.Errorf("Path = %q, want empty", result.Path)
	}
}

func TestCheckTmuxFound(t *testing.T) {
	original := lookPath
	defer func() { lookPath = original }()

	lookPath = func(file string) (string, error) {
		if file == "tmux" {
			return "/usr/bin/tmux", nil
		}
		return "", fmt.Errorf("not found")
	}

	if !CheckTmux() {
		t.Error("expected CheckTmux() = true when tmux found")
	}
}

func TestCheckTmuxNotFound(t *testing.T) {
	original := lookPath
	defer func() { lookPath = original }()

	lookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	if CheckTmux() {
		t.Error("expected CheckTmux() = false when tmux not found")
	}
}

func TestFindOpenClaw(t *testing.T) {
	tmpDir := t.TempDir()

	originalHome := userHomeDir
	defer func() { userHomeDir = originalHome }()

	userHomeDir = func() (string, error) { return tmpDir, nil }

	// Create .openclaw in home
	ocDir := filepath.Join(tmpDir, ".openclaw")
	os.MkdirAll(ocDir, 0755)

	result := FindOpenClaw()
	if result != ocDir {
		t.Errorf("FindOpenClaw() = %q, want %q", result, ocDir)
	}
}

func TestFindOpenClawNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	originalHome := userHomeDir
	defer func() { userHomeDir = originalHome }()

	userHomeDir = func() (string, error) { return tmpDir, nil }

	result := FindOpenClaw()
	if result != "" {
		t.Errorf("FindOpenClaw() = %q, want empty", result)
	}
}
