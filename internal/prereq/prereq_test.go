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

func TestFindExistingWorkspaces(t *testing.T) {
	tmpDir := t.TempDir()

	originalHome := userHomeDir
	defer func() { userHomeDir = originalHome }()

	userHomeDir = func() (string, error) { return tmpDir, nil }

	// Create a workspace with leo.yaml
	wsDir := filepath.Join(tmpDir, "my-agent")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "leo.yaml"), []byte("agent:\n  name: test\n"), 0644)

	// Create another workspace
	ws2Dir := filepath.Join(tmpDir, "agent2")
	os.MkdirAll(ws2Dir, 0755)
	os.WriteFile(filepath.Join(ws2Dir, "leo.yaml"), []byte("agent:\n  name: test2\n"), 0644)

	result := FindExistingWorkspaces()
	if len(result) != 2 {
		t.Fatalf("FindExistingWorkspaces() returned %d, want 2", len(result))
	}

	found := map[string]bool{}
	for _, ws := range result {
		found[ws] = true
	}

	if !found[wsDir] {
		t.Errorf("missing workspace %q", wsDir)
	}
	if !found[ws2Dir] {
		t.Errorf("missing workspace %q", ws2Dir)
	}
}

func TestFindExistingWorkspacesNone(t *testing.T) {
	tmpDir := t.TempDir()

	originalHome := userHomeDir
	defer func() { userHomeDir = originalHome }()

	userHomeDir = func() (string, error) { return tmpDir, nil }

	result := FindExistingWorkspaces()
	if len(result) != 0 {
		t.Errorf("FindExistingWorkspaces() returned %d, want 0", len(result))
	}
}
