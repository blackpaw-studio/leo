package run

import (
	"os"
	"os/exec"
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
		HomePath: dir,
		Telegram: config.TelegramConfig{
			BotToken: "123:ABC",
			ChatID:   "456",
			GroupID:  "-100999",
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
				Workspace:  dir,
				PromptFile: "HEARTBEAT.md",
				TopicID:    42,
			},
			wantSilent: false,
			wantTopic:  true,
		},
		{
			name: "silent task",
			task: config.TaskConfig{
				Workspace:  dir,
				PromptFile: "HEARTBEAT.md",
				TopicID:    42,
				Silent:     true,
			},
			wantSilent: true,
			wantTopic:  true,
		},
		{
			name: "no topic",
			task: config.TaskConfig{
				Workspace:  dir,
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

func TestAssemblePromptPathTraversal(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
		Telegram: config.TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
	}

	task := config.TaskConfig{Workspace: dir, PromptFile: "../../../etc/passwd"}

	_, err := assemblePrompt(cfg, task)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("error = %q, want to contain 'escapes workspace'", err.Error())
	}
}

func TestAssemblePromptMissingFile(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
		Telegram: config.TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
	}

	task := config.TaskConfig{Workspace: dir, PromptFile: "nonexistent.md"}

	_, err := assemblePrompt(cfg, task)
	if err == nil {
		t.Error("expected error for missing prompt file")
	}
}

func makeTestConfig(dir string, bypassPermissions bool) *config.Config {
	return &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{
			Model:             "sonnet",
			MaxTurns:          15,
			BypassPermissions: bypassPermissions,
		},
	}
}

func TestBuildArgs(t *testing.T) {
	dir := t.TempDir()
	// MCP config must be at <workspace>/config/mcp-servers.json.
	// Default workspace is <HomePath>/workspace, so create it there.
	mcpDir := filepath.Join(dir, "workspace", "config")
	os.MkdirAll(mcpDir, 0755)
	os.WriteFile(filepath.Join(mcpDir, "mcp-servers.json"), []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)

	cfg := makeTestConfig(dir, true)
	task := config.TaskConfig{Model: "opus", MaxTurns: 20}
	args := buildArgs(cfg, task, "test prompt", "")

	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--agent") {
		t.Error("should not contain --agent flag")
	}
	if !strings.Contains(argsStr, "--model opus") {
		t.Error("should use task model override")
	}
	if !strings.Contains(argsStr, "--max-turns 20") {
		t.Error("should use task max-turns override")
	}
	if !strings.Contains(argsStr, "--dangerously-skip-permissions") {
		t.Error("missing permissions flag when bypass_permissions is true")
	}
	if !strings.Contains(argsStr, "--mcp-config") {
		t.Error("missing mcp-config when file exists")
	}
	if !strings.Contains(argsStr, "--add-dir") {
		t.Error("missing add-dir flag")
	}
	if !strings.Contains(argsStr, "--output-format json") {
		t.Error("should use json output format")
	}
}

func TestBuildArgsWithoutBypassPermissions(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "test prompt", "")
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--dangerously-skip-permissions") {
		t.Error("should not contain --dangerously-skip-permissions when bypass_permissions is false")
	}
}

func TestBuildArgsWithoutMCPConfig(t *testing.T) {
	dir := t.TempDir()
	// No mcp-servers.json created

	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "test prompt", "")
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--mcp-config") {
		t.Error("should not contain --mcp-config when file doesn't exist")
	}
	if !strings.Contains(argsStr, "--model sonnet") {
		t.Error("should use default model")
	}
	if !strings.Contains(argsStr, "--max-turns 15") {
		t.Error("should use default max-turns")
	}
}

func TestBuildArgsWithSessionID(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "test prompt", "session-abc-123")
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--resume session-abc-123") {
		t.Error("should contain --resume with session ID")
	}
}

func TestBuildArgsWithoutSessionID(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "test prompt", "")
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--resume") {
		t.Error("should not contain --resume without session ID")
	}
	if strings.Contains(argsStr, "--continue") {
		t.Error("should not contain --continue")
	}
}

func TestParseClaudeOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantSID   string
		wantText  string
		wantError bool
	}{
		{
			name:     "valid JSON",
			output:   `{"session_id":"abc-123","result":"Hello world","is_error":false}`,
			wantSID:  "abc-123",
			wantText: "Hello world",
		},
		{
			name:     "error response",
			output:   `{"session_id":"def-456","result":"failed","is_error":true}`,
			wantSID:  "def-456",
			wantText: "failed",
		},
		{
			name:    "invalid JSON",
			output:  "not json at all",
			wantSID: "",
		},
		{
			name:    "empty",
			output:  "",
			wantSID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseClaudeOutput([]byte(tt.output))
			if result.SessionID != tt.wantSID {
				t.Errorf("SessionID = %q, want %q", result.SessionID, tt.wantSID)
			}
			if result.Result != tt.wantText {
				t.Errorf("Result = %q, want %q", result.Result, tt.wantText)
			}
		})
	}
}

func TestIsSessionError(t *testing.T) {
	tests := []struct {
		name   string
		result claudeResult
		output string
		want   bool
	}{
		{
			name:   "session not found in result",
			result: claudeResult{Result: "Session not found"},
			want:   true,
		},
		{
			name:   "invalid session in output",
			result: claudeResult{},
			output: "Error: invalid session ID",
			want:   true,
		},
		{
			name:   "expired session",
			result: claudeResult{Result: "session expired"},
			want:   true,
		},
		{
			name:   "unrelated error",
			result: claudeResult{Result: "model overloaded"},
			want:   false,
		},
		{
			name:   "empty",
			result: claudeResult{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSessionError(tt.result, []byte(tt.output))
			if got != tt.want {
				t.Errorf("isSessionError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	dir := t.TempDir()
	// Default workspace is <HomePath>/workspace; create prompt file there.
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("Check inbox"), 0644)

	cfg := &config.Config{
		HomePath: dir,
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

	prompt, args, err := Preview(cfg, "heartbeat", nil)
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

	_, _, err := Preview(cfg, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestRunTaskNotFound(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{},
	}

	err := Run(cfg, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestRunSuccess(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	// Default workspace is <HomePath>/workspace; create prompt file there.
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "task output")
	}

	cfg := &config.Config{
		HomePath: dir,
		Telegram: config.TelegramConfig{BotToken: "t", ChatID: "c"},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify log was written; StatePath() = <HomePath>/state
	logData, err := os.ReadFile(filepath.Join(dir, "state", "mytask.log"))
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if !strings.Contains(string(logData), "task output") {
		t.Errorf("log = %q, want to contain 'task output'", string(logData))
	}
}

func TestRunCommandError(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	cfg := &config.Config{
		HomePath: dir,
		Telegram: config.TelegramConfig{BotToken: "t", ChatID: "c"},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error when command fails")
	}
	if !strings.Contains(err.Error(), "claude exited with error") {
		t.Errorf("error = %q, want to contain 'claude exited with error'", err.Error())
	}
}

func TestRunMissingPromptFile(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
		Telegram: config.TelegramConfig{BotToken: "t", ChatID: "c"},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "nonexistent.md", Schedule: "0 * * * *"},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error for missing prompt file")
	}
	if !strings.Contains(err.Error(), "assembling prompt") {
		t.Errorf("error = %q, want to contain 'assembling prompt'", err.Error())
	}
}

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
	}

	if err := writeLog(cfg, "test-task", []byte("test output")); err != nil {
		t.Fatal(err)
	}

	// StatePath() = <HomePath>/state
	logPath := filepath.Join(dir, "state", "test-task.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "test output" {
		t.Errorf("log content = %q, want %q", string(data), "test output")
	}
}
