package onboard

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSmokeTestSuccess(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		// Verify the right command is being built
		if name != "claude" {
			t.Errorf("command = %q, want %q", name, "claude")
		}
		// Use a real command that echoes the expected output
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	err := SmokeTest()
	if err != nil {
		t.Fatalf("SmokeTest() error: %v", err)
	}
}

func TestSmokeTestBadOutput(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "something else")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for bad output")
	}

	if !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("error = %q, want to contain 'unexpected'", err.Error())
	}
}

func TestSmokeTestCommandFailure(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for command failure")
	}

	if !strings.Contains(err.Error(), "smoke test failed") {
		t.Errorf("error = %q, want to contain 'smoke test failed'", err.Error())
	}
}

func TestReconfigureTasksAddHeartbeat(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    make(map[string]config.TaskConfig),
	}

	// Save initial config
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Simulate: "y" to add heartbeat
	reader := bufio.NewReader(strings.NewReader("y\n"))

	err := reconfigureTasks(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTasks() error: %v", err)
	}

	if _, ok := cfg.Tasks["heartbeat"]; !ok {
		t.Error("expected heartbeat task to be added")
	}

	// Verify it was saved
	loaded, err := config.LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Tasks["heartbeat"]; !ok {
		t.Error("heartbeat task should be persisted")
	}
}

func TestReconfigureTasksSkipExisting(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {
				Schedule:   "0 * * * *",
				PromptFile: "HEARTBEAT.md",
				Enabled:    true,
			},
		},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(strings.NewReader("\n"))

	err := reconfigureTasks(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTasks() error: %v", err)
	}
}

func TestReconfigureSingleWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Choose option 2 (tasks), then "n" to skip heartbeat
	reader := bufio.NewReader(strings.NewReader("2\nn\n"))

	err := reconfigure(reader, []string{dir})
	if err != nil {
		t.Fatalf("reconfigure() error: %v", err)
	}
}

func TestReconfigureTelegramUpdate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "old-token",
			ChatID:   "old-chat",
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Enter new token, chat ID, no group, and "n" to skip test message
	reader := bufio.NewReader(strings.NewReader("new-token\nnew-chat\n\nn\n"))

	err := reconfigureTelegram(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTelegram() error: %v", err)
	}

	if cfg.Telegram.BotToken != "new-token" {
		t.Errorf("BotToken = %q, want %q", cfg.Telegram.BotToken, "new-token")
	}
	if cfg.Telegram.ChatID != "new-chat" {
		t.Errorf("ChatID = %q, want %q", cfg.Telegram.ChatID, "new-chat")
	}

	// Verify saved
	loaded, err := config.LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telegram.BotToken != "new-token" {
		t.Error("new token should be persisted")
	}
}

func TestSmokeTestArgs(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	SmokeTest()

	expected := []string{"-p", "Reply with exactly: LEO_SMOKE_OK", "--max-turns", "1", "--output-format", "text"}
	if len(gotArgs) != len(expected) {
		t.Fatalf("args count = %d, want %d", len(gotArgs), len(expected))
	}

	for i, arg := range expected {
		if gotArgs[i] != arg {
			t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], arg)
		}
	}
}
