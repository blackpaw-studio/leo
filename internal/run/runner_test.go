package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestAssemblePrompt(t *testing.T) {
	dir := t.TempDir()

	promptContent := "Check the inbox and summarize new emails."
	promptFile := filepath.Join(dir, "HEARTBEAT.md")
	os.WriteFile(promptFile, []byte(promptContent), 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "myagent",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "123:ABC",
			ChatID:   "456",
			GroupID:  "-100999",
			Topics:   map[string]int{"alerts": 42},
		},
	}

	tests := []struct {
		name       string
		task       config.TaskConfig
		wantSilent bool
		wantTopic  bool
	}{
		{
			name: "basic task",
			task: config.TaskConfig{
				PromptFile: "HEARTBEAT.md",
				Topic:      "alerts",
			},
			wantSilent: false,
			wantTopic:  true,
		},
		{
			name: "silent task",
			task: config.TaskConfig{
				PromptFile: "HEARTBEAT.md",
				Topic:      "alerts",
				Silent:     true,
			},
			wantSilent: true,
			wantTopic:  true,
		},
		{
			name: "no topic",
			task: config.TaskConfig{
				PromptFile: "HEARTBEAT.md",
			},
			wantSilent: false,
			wantTopic:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := assemblePrompt(cfg, tt.task)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(prompt, promptContent) {
				t.Error("prompt should contain prompt file content")
			}

			if !strings.Contains(prompt, "123:ABC") {
				t.Error("prompt should contain bot token")
			}

			// Should use group_id when set
			if !strings.Contains(prompt, "-100999") {
				t.Error("prompt should use group_id as chat_id")
			}

			if tt.wantSilent && !strings.Contains(prompt, "SILENT SCHEDULED RUN") {
				t.Error("silent task should contain preamble")
			}
			if !tt.wantSilent && strings.Contains(prompt, "SILENT SCHEDULED RUN") {
				t.Error("non-silent task should not contain preamble")
			}

			if tt.wantTopic && !strings.Contains(prompt, "message_thread_id") {
				t.Error("task with topic should contain message_thread_id")
			}
			if !tt.wantTopic && strings.Contains(prompt, "message_thread_id") {
				t.Error("task without topic should not contain message_thread_id")
			}
		})
	}
}

func TestAssemblePromptMissingFile(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
	}

	task := config.TaskConfig{PromptFile: "nonexistent.md"}

	_, err := assemblePrompt(cfg, task)
	if err == nil {
		t.Error("expected error for missing prompt file")
	}
}

func TestBuildArgs(t *testing.T) {
	dir := t.TempDir()
	mcpDir := filepath.Join(dir, "config")
	os.MkdirAll(mcpDir, 0755)
	os.WriteFile(filepath.Join(mcpDir, "mcp-servers.json"), []byte("{}"), 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "myagent",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
		},
	}

	task := config.TaskConfig{Model: "opus", MaxTurns: 20}
	args := buildArgs(cfg, task, "test prompt")

	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--agent myagent") {
		t.Error("missing agent flag")
	}
	if !strings.Contains(argsStr, "--model opus") {
		t.Error("should use task model override")
	}
	if !strings.Contains(argsStr, "--max-turns 20") {
		t.Error("should use task max-turns override")
	}
	if !strings.Contains(argsStr, "--dangerously-skip-permissions") {
		t.Error("missing permissions flag")
	}
	if !strings.Contains(argsStr, "--mcp-config") {
		t.Error("missing mcp-config when file exists")
	}
	if !strings.Contains(argsStr, "--add-dir") {
		t.Error("missing add-dir flag")
	}
}

func TestBuildArgsWithoutMCPConfig(t *testing.T) {
	dir := t.TempDir()
	// No mcp-servers.json created

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "myagent",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
		},
	}

	task := config.TaskConfig{}
	args := buildArgs(cfg, task, "test prompt")
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--mcp-config") {
		t.Error("should not contain --mcp-config when file doesn't exist")
	}

	// Should use default model
	if !strings.Contains(argsStr, "--model sonnet") {
		t.Error("should use default model")
	}

	// Should use default max-turns
	if !strings.Contains(argsStr, "--max-turns 15") {
		t.Error("should use default max-turns")
	}
}

func TestPreview(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "HEARTBEAT.md")
	os.WriteFile(promptFile, []byte("Check inbox"), 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "myagent",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "123:ABC",
			ChatID:   "456",
		},
		Defaults: config.DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {
				Schedule:   "0 * * * *",
				PromptFile: "HEARTBEAT.md",
				Model:      "opus",
			},
		},
	}

	prompt, args, err := Preview(cfg, "heartbeat")
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}

	if !strings.Contains(prompt, "Check inbox") {
		t.Error("prompt should contain file content")
	}

	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--model opus") {
		t.Error("args should contain task model override")
	}
}

func TestPreviewTaskNotFound(t *testing.T) {
	cfg := &config.Config{Tasks: map[string]config.TaskConfig{}}

	_, _, err := Preview(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestRunTaskNotFound(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{},
	}

	err := Run(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: dir,
		},
	}

	if err := writeLog(cfg, "test-task", []byte("test output")); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "state", "test-task.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "test output" {
		t.Errorf("log content = %q, want %q", string(data), "test output")
	}
}
