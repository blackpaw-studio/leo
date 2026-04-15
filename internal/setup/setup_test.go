package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/service"
)

// --- buildConfig ---

func TestBuildConfig_FreshWorkspace(t *testing.T) {
	cfg := buildConfig("/my/workspace", nil)

	if cfg.Defaults.Model != config.DefaultModel {
		t.Errorf("Defaults.Model = %q, want %q", cfg.Defaults.Model, config.DefaultModel)
	}
	if cfg.Defaults.MaxTurns != config.DefaultMaxTurns {
		t.Errorf("Defaults.MaxTurns = %d, want %d", cfg.Defaults.MaxTurns, config.DefaultMaxTurns)
	}
	proc, ok := cfg.Processes["assistant"]
	if !ok {
		t.Fatal("expected default 'assistant' process")
	}
	if proc.Workspace != "/my/workspace" {
		t.Errorf("process workspace = %q, want %q", proc.Workspace, "/my/workspace")
	}
	if !proc.Enabled {
		t.Error("default process should be enabled")
	}
	if proc.RemoteControl == nil || !*proc.RemoteControl {
		t.Error("default process should have remote_control enabled")
	}
	if len(proc.Channels) != 0 {
		t.Errorf("expected empty channels (channel-agnostic default), got %v", proc.Channels)
	}
}

func TestBuildConfig_PreservesExistingConfig(t *testing.T) {
	existing := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:    "opus",
			MaxTurns: 99,
		},
		Processes: map[string]config.ProcessConfig{
			"custom": {
				Workspace: "/custom/ws",
				Channels:  []string{"plugin:telegram@claude-plugins-official"},
				Enabled:   true,
			},
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "0 * * * *", PromptFile: "x.md"},
		},
	}

	cfg := buildConfig("/my/workspace", existing)

	if cfg.Defaults.Model != "opus" {
		t.Errorf("Defaults.Model = %q, want 'opus'", cfg.Defaults.Model)
	}
	if _, ok := cfg.Processes["custom"]; !ok {
		t.Error("expected existing 'custom' process preserved")
	}
	if _, ok := cfg.Tasks["heartbeat"]; !ok {
		t.Error("expected existing 'heartbeat' task preserved")
	}
}

// --- parseUserProfile ---

func TestParseUserProfile_FullFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "USER.md")
	content := `# User Profile

## Name
Alice

## Role
Engineer

## About
Builds things

## Preferences
Dark mode

## Timezone
America/New_York
`
	os.WriteFile(path, []byte(content), 0644)

	got := parseUserProfile(path)
	if got.UserName != "Alice" {
		t.Errorf("UserName = %q, want %q", got.UserName, "Alice")
	}
	if got.Role != "Engineer" {
		t.Errorf("Role = %q, want %q", got.Role, "Engineer")
	}
	if got.About != "Builds things" {
		t.Errorf("About = %q, want %q", got.About, "Builds things")
	}
	if got.Preferences != "Dark mode" {
		t.Errorf("Preferences = %q, want %q", got.Preferences, "Dark mode")
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want %q", got.Timezone, "America/New_York")
	}
}

func TestParseUserProfile_MissingFile(t *testing.T) {
	got := parseUserProfile("/nonexistent/path.md")
	if got.UserName != "" {
		t.Errorf("UserName = %q, want empty for missing file", got.UserName)
	}
}

// --- checkWorkspaceWritable ---

func TestCheckWorkspaceWritable_Success(t *testing.T) {
	dir := t.TempDir()
	if err := checkWorkspaceWritable(filepath.Join(dir, "new")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- findExistingConfig ---

func TestFindExistingConfig_None(t *testing.T) {
	dir := t.TempDir()
	cfg, defaultHome := findExistingConfig(dir)
	if cfg != nil {
		t.Error("expected nil config for empty home")
	}
	if defaultHome != filepath.Join(dir, ".leo") {
		t.Errorf("defaultHome = %q, want %q", defaultHome, filepath.Join(dir, ".leo"))
	}
}

func TestFindExistingConfig_Found(t *testing.T) {
	dir := t.TempDir()
	leoHome := filepath.Join(dir, ".leo")
	os.MkdirAll(leoHome, 0750)

	cfg := &config.Config{
		Defaults:  config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Processes: map[string]config.ProcessConfig{"assistant": {Enabled: true}},
	}
	if err := config.Save(filepath.Join(leoHome, "leo.yaml"), cfg); err != nil {
		t.Fatalf("saving config: %v", err)
	}

	got, _ := findExistingConfig(dir)
	if got == nil {
		t.Fatal("expected config to be found")
	}
	if got.Defaults.Model != "sonnet" {
		t.Errorf("Model = %q, want 'sonnet'", got.Defaults.Model)
	}
}

// --- scaffoldWorkspace ---

func TestScaffoldWorkspace_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	leoHome := filepath.Join(dir, ".leo")
	workspace := filepath.Join(leoHome, "workspace")

	cfg := buildConfig(workspace, nil)

	opts := scaffoldOptions{
		workspace: workspace,
		home:      dir,
		leoHome:   leoHome,
		cfg:       cfg,
		userPath:  filepath.Join(workspace, "USER.md"),
		userName:  "Test User",
		role:      "Developer",
		about:     "Writes tests",
		timezone:  "UTC",
	}

	if err := scaffoldWorkspace(opts); err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(leoHome, "leo.yaml")); err != nil {
		t.Errorf("expected leo.yaml in leo home: %v", err)
	}

	for _, rel := range []string{
		"USER.md",
		"CLAUDE.md",
		"skills/managing-tasks.md",
		"config/mcp-servers.json",
	} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Errorf("expected %s in workspace: %v", rel, err)
		}
	}

	claudeData, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if strings.Contains(string(claudeData), "Telegram Messaging Rules") {
		t.Error("CLAUDE.md should not reference telegram-specific rules")
	}
	if !strings.Contains(string(claudeData), "LEO_CHANNELS") {
		t.Error("CLAUDE.md should reference LEO_CHANNELS env var")
	}
}

// --- checkPrerequisites ---

func TestCheckPrerequisites_ClaudeMissing(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }
	checkTmuxFn = func() bool { return true }

	if err := checkPrerequisites(); err == nil {
		t.Error("expected error when claude missing")
	}
}

func TestCheckPrerequisites_TmuxMissing(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Path: "/usr/bin/claude", Version: "1.0.0"}
	}
	checkTmuxFn = func() bool { return false }

	if err := checkPrerequisites(); err == nil {
		t.Error("expected error when tmux missing")
	}
}

func TestCheckPrerequisites_AllPresent(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Path: "/usr/bin/claude", Version: "1.0.0"}
	}
	checkTmuxFn = func() bool { return true }

	if err := checkPrerequisites(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- installDaemon ---

func TestInstallDaemon_Success(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{"PATH": "/usr/bin"} }
	installDaemonFn = func(sc service.ServiceConfig) error { return nil }
	daemonStatusFn = func() (string, error) { return "running", nil }

	installDaemon("/tmp/workspace", "/tmp/workspace/leo.yaml")
}

func TestInstallDaemon_Failure(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{} }
	installDaemonFn = func(sc service.ServiceConfig) error { return fmt.Errorf("install failed") }

	installDaemon("/tmp/workspace", "/tmp/workspace/leo.yaml")
}

func TestInstallDaemon_NoExecutable(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "", fmt.Errorf("no executable") }
	envCaptureFn = func() map[string]string { return map[string]string{} }

	var capturedSC service.ServiceConfig
	installDaemonFn = func(sc service.ServiceConfig) error {
		capturedSC = sc
		return nil
	}
	daemonStatusFn = func() (string, error) { return "running", nil }

	installDaemon("/tmp/ws", "/tmp/ws/leo.yaml")
	if capturedSC.LeoPath != "leo" {
		t.Errorf("LeoPath = %q, want %q (fallback)", capturedSC.LeoPath, "leo")
	}
}
