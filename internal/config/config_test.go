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
