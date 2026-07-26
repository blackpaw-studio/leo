package prereq

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckBinary(t *testing.T) {
	tests := []struct {
		name        string
		lookPathFn  func(file string) (string, error)
		runCommand  func(path string, args ...string) ([]byte, error)
		wantOK      bool
		wantPath    string
		wantVersion string
	}{
		{
			name: "found with version",
			lookPathFn: func(file string) (string, error) {
				return "/usr/local/bin/claude", nil
			},
			runCommand: func(path string, args ...string) ([]byte, error) {
				return []byte("claude 1.0.0\n"), nil
			},
			wantOK:      true,
			wantPath:    "/usr/local/bin/claude",
			wantVersion: "claude 1.0.0",
		},
		{
			name: "found but version command fails",
			lookPathFn: func(file string) (string, error) {
				return "/usr/local/bin/claude", nil
			},
			runCommand: func(path string, args ...string) ([]byte, error) {
				return nil, fmt.Errorf("exit status 1")
			},
			wantOK:      true,
			wantPath:    "/usr/local/bin/claude",
			wantVersion: "",
		},
		{
			name: "not found",
			lookPathFn: func(file string) (string, error) {
				return "", fmt.Errorf("not found")
			},
			runCommand: func(path string, args ...string) ([]byte, error) {
				t.Fatal("runCommand should not be called when binary is not found")
				return nil, nil
			},
			wantOK:      false,
			wantPath:    "",
			wantVersion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := lookPath
			originalRun := runCommand
			defer func() {
				lookPath = original
				runCommand = originalRun
			}()

			lookPath = tt.lookPathFn
			runCommand = tt.runCommand

			result := CheckBinary("claude")

			if result.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v", result.OK, tt.wantOK)
			}
			if result.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", result.Path, tt.wantPath)
			}
			if result.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", result.Version, tt.wantVersion)
			}
		})
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

// TestTmuxVersionAtLeast covers the version gate for `new-session -e`, which
// leo relies on to keep credentials out of the pane's start command. Without
// it an old tmux fails every spawn and the supervisor retries forever, which
// looks like "agents silently never start".
func TestTmuxVersionAtLeast(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"tmux 3.2", true},
		{"tmux 3.2a", true},
		{"tmux 3.6a", true},
		{"tmux 4.0", true},
		{"tmux 10.1", true},
		{"tmux 3.1c", false},
		{"tmux 3.0a", false},
		{"tmux 2.8", false},
		{"tmux next-3.4", true},
		{"tmux master", true}, // unknown/dev build: assume capable
		{"", true},            // unparseable: don't block on a guess
		{"garbage 1", true},   // no X.Y to read: same policy
		{"tmux 1.9a", false},  // parseable and genuinely too old
	}
	for _, tt := range tests {
		if got := tmuxVersionAtLeast(tt.raw, 3, 2); got != tt.want {
			t.Errorf("tmuxVersionAtLeast(%q, 3, 2) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
