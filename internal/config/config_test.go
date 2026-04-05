package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testYAML = `
agent:
  name: myagent
  workspace: /tmp/test-workspace
  agent_file: ~/.claude/agents/myagent.md

telegram:
  bot_token: "123:ABC"
  chat_id: "456"
  group_id: "-100999"
  topics:
    alerts: 1
    news: 3

defaults:
  model: sonnet
  max_turns: 15

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md
    model: sonnet
    max_turns: 10
    topic: alerts
    enabled: true
  daily-news:
    schedule: "0 7 * * *"
    prompt_file: reports/news.md
    model: opus
    max_turns: 20
    topic: news
    enabled: true
    silent: true
  disabled-task:
    schedule: "0 * * * *"
    prompt_file: reports/noop.md
    enabled: false
`

func TestValidate(t *testing.T) {
	validConfig := func() *Config {
		return &Config{
			Agent: AgentConfig{
				Name:      "myagent",
				Workspace: "/tmp/workspace",
			},
			Telegram: TelegramConfig{
				BotToken: "123:ABC",
				ChatID:   "456",
				Topics:   map[string]int{"alerts": 1},
			},
			Defaults: DefaultsConfig{
				Model:    "sonnet",
				MaxTurns: 15,
			},
			Tasks: map[string]TaskConfig{
				"heartbeat": {
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					Topic:      "alerts",
					Enabled:    true,
				},
			},
		}
	}

	t.Run("valid config passes", func(t *testing.T) {
		cfg := validConfig()
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("empty agent name", func(t *testing.T) {
		cfg := validConfig()
		cfg.Agent.Name = ""
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "agent.name is required") {
			t.Errorf("error = %q, want mention of agent.name", got)
		}
	})

	t.Run("empty agent workspace", func(t *testing.T) {
		cfg := validConfig()
		cfg.Agent.Workspace = ""
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "agent.workspace is required") {
			t.Errorf("error = %q, want mention of agent.workspace", got)
		}
	})

	t.Run("invalid default model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Defaults.Model = "gpt-4"
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "defaults.model") {
			t.Errorf("error = %q, want mention of defaults.model", got)
		}
	})

	t.Run("negative max turns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Defaults.MaxTurns = -1
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "max_turns must not be negative") {
			t.Errorf("error = %q, want mention of max_turns", got)
		}
	})

	t.Run("telegram bot token without chat id", func(t *testing.T) {
		cfg := validConfig()
		cfg.Telegram.ChatID = ""
		cfg.Telegram.GroupID = ""
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "chat_id or telegram.group_id") {
			t.Errorf("error = %q, want mention of chat_id", got)
		}
	})

	t.Run("telegram group id suffices", func(t *testing.T) {
		cfg := validConfig()
		cfg.Telegram.ChatID = ""
		cfg.Telegram.GroupID = "-100999"
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("task missing schedule", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tasks["bad"] = TaskConfig{PromptFile: "test.md"}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "tasks.bad.schedule is required") {
			t.Errorf("error = %q, want mention of tasks.bad.schedule", got)
		}
	})

	t.Run("task missing prompt file", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tasks["bad"] = TaskConfig{Schedule: "0 * * * *"}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "tasks.bad.prompt_file is required") {
			t.Errorf("error = %q, want mention of prompt_file", got)
		}
	})

	t.Run("task invalid model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tasks["heartbeat"] = TaskConfig{
			Schedule:   "0 * * * *",
			PromptFile: "HEARTBEAT.md",
			Model:      "claude-3",
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "tasks.heartbeat.model") {
			t.Errorf("error = %q, want mention of task model", got)
		}
	})

	t.Run("task references unknown topic", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tasks["heartbeat"] = TaskConfig{
			Schedule:   "0 * * * *",
			PromptFile: "HEARTBEAT.md",
			Topic:      "nonexistent",
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "topic \"nonexistent\" not found") {
			t.Errorf("error = %q, want mention of unknown topic", got)
		}
	})

	t.Run("multiple errors collected", func(t *testing.T) {
		cfg := &Config{}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		got := err.Error()
		if !contains(got, "agent.name") || !contains(got, "agent.workspace") {
			t.Errorf("expected multiple errors, got %q", got)
		}
	})

	t.Run("no telegram section is fine", func(t *testing.T) {
		cfg := &Config{
			Agent: AgentConfig{Name: "test", Workspace: "/tmp"},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("empty model is fine", func(t *testing.T) {
		cfg := &Config{
			Agent: AgentConfig{Name: "test", Workspace: "/tmp"},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Agent.Name != "myagent" {
		t.Errorf("agent name = %q, want %q", cfg.Agent.Name, "myagent")
	}

	if cfg.Agent.Workspace != "/tmp/test-workspace" {
		t.Errorf("workspace = %q, want %q", cfg.Agent.Workspace, "/tmp/test-workspace")
	}

	if cfg.Telegram.BotToken != "123:ABC" {
		t.Errorf("bot_token = %q, want %q", cfg.Telegram.BotToken, "123:ABC")
	}

	if cfg.Telegram.ChatID != "456" {
		t.Errorf("chat_id = %q, want %q", cfg.Telegram.ChatID, "456")
	}

	if cfg.Defaults.Model != "sonnet" {
		t.Errorf("default model = %q, want %q", cfg.Defaults.Model, "sonnet")
	}

	if cfg.Defaults.MaxTurns != 15 {
		t.Errorf("default max_turns = %d, want %d", cfg.Defaults.MaxTurns, 15)
	}

	if len(cfg.Tasks) != 3 {
		t.Errorf("tasks count = %d, want %d", len(cfg.Tasks), 3)
	}
}

func TestTaskModel(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
	}

	tests := []struct {
		name     string
		task     TaskConfig
		wantModel string
		wantTurns int
	}{
		{
			name:      "uses task model when set",
			task:      TaskConfig{Model: "opus", MaxTurns: 20},
			wantModel: "opus",
			wantTurns: 20,
		},
		{
			name:      "falls back to defaults",
			task:      TaskConfig{},
			wantModel: "sonnet",
			wantTurns: 15,
		},
		{
			name:      "partial override",
			task:      TaskConfig{Model: "haiku"},
			wantModel: "haiku",
			wantTurns: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.TaskModel(tt.task); got != tt.wantModel {
				t.Errorf("TaskModel() = %q, want %q", got, tt.wantModel)
			}
			if got := cfg.TaskMaxTurns(tt.task); got != tt.wantTurns {
				t.Errorf("TaskMaxTurns() = %d, want %d", got, tt.wantTurns)
			}
		})
	}
}

func TestTopicID(t *testing.T) {
	cfg := &Config{
		Telegram: TelegramConfig{
			Topics: map[string]int{"alerts": 1, "news": 3},
		},
	}

	if got := cfg.TopicID("alerts"); got != 1 {
		t.Errorf("TopicID(alerts) = %d, want 1", got)
	}
	if got := cfg.TopicID("news"); got != 3 {
		t.Errorf("TopicID(news) = %d, want 3", got)
	}
	if got := cfg.TopicID("missing"); got != 0 {
		t.Errorf("TopicID(missing) = %d, want 0", got)
	}
	if got := cfg.TopicID(""); got != 0 {
		t.Errorf("TopicID('') = %d, want 0", got)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")

	cfg := &Config{
		Agent: AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Telegram: TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
		Defaults: DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 10,
		},
		Tasks: map[string]TaskConfig{
			"heartbeat": {
				Schedule:   "* * * * *",
				PromptFile: "HEARTBEAT.md",
				Enabled:    true,
			},
		},
	}

	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	// Verify file permissions
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("file permissions = %o, want 0600", fi.Mode().Perm())
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Agent.Name != cfg.Agent.Name {
		t.Errorf("loaded name = %q, want %q", loaded.Agent.Name, cfg.Agent.Name)
	}
	if loaded.Telegram.BotToken != cfg.Telegram.BotToken {
		t.Errorf("loaded token = %q, want %q", loaded.Telegram.BotToken, cfg.Telegram.BotToken)
	}
	if len(loaded.Tasks) != 1 {
		t.Errorf("loaded tasks = %d, want 1", len(loaded.Tasks))
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	os.WriteFile(path, []byte("{{{{invalid yaml"), 0644)

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/leo.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(testYAML), 0644)

	cfg, err := LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Agent.Name != "myagent" {
		t.Errorf("agent name = %q, want %q", cfg.Agent.Name, "myagent")
	}
}

func TestMCPConfigPath(t *testing.T) {
	cfg := &Config{
		Agent: AgentConfig{Workspace: "/home/user/myagent"},
	}

	got := cfg.MCPConfigPath()
	want := filepath.Join("/home/user/myagent", "config", "mcp-servers.json")
	if got != want {
		t.Errorf("MCPConfigPath() = %q, want %q", got, want)
	}
}

func TestFindConfig(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(subdir, 0755)

	// No config yet
	_, err := FindConfig(subdir)
	if err == nil {
		t.Error("expected error when no config found")
	}

	// Create config at root
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte("agent:\n  name: test\n"), 0644)

	found, err := FindConfig(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("found = %q, want %q", found, cfgPath)
	}
}
