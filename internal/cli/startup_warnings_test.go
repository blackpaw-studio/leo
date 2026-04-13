package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestStartupWarnings_NilConfig(t *testing.T) {
	if got := startupWarnings(nil); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestStartupWarnings_MissingDefaultWorkspace(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	// DefaultWorkspace returns <home>/workspace, which we haven't created.
	warnings := startupWarnings(cfg)
	if len(warnings) == 0 {
		t.Fatal("expected at least one warning for missing default workspace")
	}
	if !strings.Contains(warnings[0], "default workspace") {
		t.Errorf("warnings[0] = %q, want mention of default workspace", warnings[0])
	}
}

func TestStartupWarnings_MissingProcessWorkspace(t *testing.T) {
	home := t.TempDir()
	// Create default workspace so only the process workspace is missing.
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0750); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		HomePath: home,
		Processes: map[string]config.ProcessConfig{
			"bad":      {Enabled: true, Workspace: "/does/not/exist"},
			"disabled": {Enabled: false, Workspace: "/also/missing/but/disabled"},
		},
	}
	warnings := startupWarnings(cfg)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], `"bad"`) {
		t.Errorf("warnings[0] = %q, want mention of process \"bad\"", warnings[0])
	}
}

func TestStartupWarnings_MissingPromptFile(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	if err := os.MkdirAll(ws, 0750); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		HomePath: home,
		Tasks: map[string]config.TaskConfig{
			"missing-prompt":  {Enabled: true, PromptFile: "not-there.md"},
			"disabled-prompt": {Enabled: false, PromptFile: "also-not-there.md"},
		},
	}
	warnings := startupWarnings(cfg)
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "missing-prompt") {
			found = true
		}
		if strings.Contains(w, "disabled-prompt") {
			t.Errorf("disabled task should not warn: %q", w)
		}
	}
	if !found {
		t.Error("expected warning for missing-prompt task")
	}
}

func TestStartupWarnings_CleanConfig(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	if err := os.MkdirAll(ws, 0750); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(ws, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		HomePath: home,
		Tasks: map[string]config.TaskConfig{
			"ok": {Enabled: true, PromptFile: "prompt.md"},
		},
	}
	if got := startupWarnings(cfg); len(got) != 0 {
		t.Errorf("want no warnings, got %v", got)
	}
}
