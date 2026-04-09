package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testYAML = `
telegram:
  bot_token: "123:ABC"
  chat_id: "456"
  group_id: "-100999"

defaults:
  model: sonnet
  max_turns: 15

processes:
  assistant:
    channels:
      - "plugin:telegram@claude-plugins-official"
    remote_control: true
    enabled: true

  researcher:
    workspace: /tmp/research
    model: opus
    add_dirs:
      - /tmp/data
    enabled: true

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md
    model: sonnet
    max_turns: 10
    topic_id: 1
    enabled: true
  daily-news:
    schedule: "0 7 * * *"
    prompt_file: reports/news.md
    model: opus
    max_turns: 20
    topic_id: 3
    enabled: true
    silent: true
  disabled-task:
    schedule: "0 * * * *"
    prompt_file: reports/noop.md
    enabled: false
`

func TestValidate(t *testing.T) {
	validConfig := func() *Config {
		tr := true
		return &Config{
			Telegram: TelegramConfig{
				BotToken: "123:ABC",
				ChatID:   "456",
			},
			Defaults: DefaultsConfig{
				Model:    "sonnet",
				MaxTurns: 15,
			},
			Processes: map[string]ProcessConfig{
				"assistant": {
					Channels:      []string{"plugin:telegram@claude-plugins-official"},
					RemoteControl: &tr,
					Enabled:       true,
				},
			},
			Tasks: map[string]TaskConfig{
				"heartbeat": {
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					TopicID:    1,
					Enabled:    true,
				},
			},
			HomePath: "/tmp/leo",
		}
	}

	t.Run("valid config passes", func(t *testing.T) {
		cfg := validConfig()
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
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

	t.Run("process invalid model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Processes["bad"] = ProcessConfig{Model: "gpt-4", Enabled: true}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "processes.bad.model") {
			t.Errorf("error = %q, want mention of processes.bad.model", got)
		}
	})

	t.Run("process negative max turns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Processes["bad"] = ProcessConfig{MaxTurns: -1, Enabled: true}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !contains(got, "processes.bad.max_turns") {
			t.Errorf("error = %q, want mention of max_turns", got)
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

	t.Run("empty config is valid", func(t *testing.T) {
		cfg := &Config{}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("no telegram section is fine", func(t *testing.T) {
		cfg := &Config{HomePath: "/tmp/leo"}
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

func TestValidateCronExpr(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"valid simple", "0 7 * * *", false},
		{"valid ranges", "0,30 7-22 * * *", false},
		{"valid step", "*/5 * * * *", false},
		{"valid complex", "0 7 1-15 1,6 1-5", false},
		{"too few fields", "0 7 * *", true},
		{"too many fields", "0 7 * * * *", true},
		{"invalid minute", "60 * * * *", true},
		{"invalid hour", "0 25 * * *", true},
		{"invalid day", "0 0 32 * *", true},
		{"invalid month", "0 0 * 13 *", true},
		{"invalid dow", "0 0 * * 8", true},
		{"non-numeric", "abc * * * *", true},
		{"negative value", "0 -1 * * *", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCronExpr(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCronExpr(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsBadSchedule(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"bad": {Schedule: "not a cron", PromptFile: "test.md"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad schedule")
	}
	if got := err.Error(); !contains(got, "tasks.bad.schedule") {
		t.Errorf("error = %q, want mention of schedule", got)
	}
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

	if cfg.HomePath != dir {
		t.Errorf("HomePath = %q, want %q", cfg.HomePath, dir)
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

	if len(cfg.Processes) != 2 {
		t.Errorf("processes count = %d, want %d", len(cfg.Processes), 2)
	}

	if len(cfg.Tasks) != 3 {
		t.Errorf("tasks count = %d, want %d", len(cfg.Tasks), 3)
	}

	// Check process workspace was kept
	if ws := cfg.Processes["researcher"].Workspace; ws != "/tmp/research" {
		t.Errorf("researcher workspace = %q, want /tmp/research", ws)
	}

	// Check assistant has no explicit workspace (defaults apply)
	if ws := cfg.Processes["assistant"].Workspace; ws != "" {
		t.Errorf("assistant workspace = %q, want empty", ws)
	}
}

func TestDefaultWorkspace(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}
	want := "/home/user/.leo/workspace"
	if got := cfg.DefaultWorkspace(); got != want {
		t.Errorf("DefaultWorkspace() = %q, want %q", got, want)
	}
}

func TestProcessWorkspace(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}

	t.Run("explicit workspace", func(t *testing.T) {
		p := ProcessConfig{Workspace: "/custom/workspace"}
		if got := cfg.ProcessWorkspace(p); got != "/custom/workspace" {
			t.Errorf("ProcessWorkspace() = %q, want /custom/workspace", got)
		}
	})

	t.Run("default workspace", func(t *testing.T) {
		p := ProcessConfig{}
		want := "/home/user/.leo/workspace"
		if got := cfg.ProcessWorkspace(p); got != want {
			t.Errorf("ProcessWorkspace() = %q, want %q", got, want)
		}
	})
}

func TestProcessDefaults(t *testing.T) {
	tr := true
	fa := false

	cfg := &Config{
		Defaults: DefaultsConfig{
			Model:             "sonnet",
			MaxTurns:          15,
			BypassPermissions: true,
			RemoteControl:     false,
		},
	}

	t.Run("process model override", func(t *testing.T) {
		p := ProcessConfig{Model: "opus"}
		if got := cfg.ProcessModel(p); got != "opus" {
			t.Errorf("ProcessModel() = %q, want opus", got)
		}
	})

	t.Run("process model default", func(t *testing.T) {
		p := ProcessConfig{}
		if got := cfg.ProcessModel(p); got != "sonnet" {
			t.Errorf("ProcessModel() = %q, want sonnet", got)
		}
	})

	t.Run("process max_turns override", func(t *testing.T) {
		p := ProcessConfig{MaxTurns: 30}
		if got := cfg.ProcessMaxTurns(p); got != 30 {
			t.Errorf("ProcessMaxTurns() = %d, want 30", got)
		}
	})

	t.Run("process max_turns default", func(t *testing.T) {
		p := ProcessConfig{}
		if got := cfg.ProcessMaxTurns(p); got != 15 {
			t.Errorf("ProcessMaxTurns() = %d, want 15", got)
		}
	})

	t.Run("process bypass override true", func(t *testing.T) {
		p := ProcessConfig{BypassPermissions: &tr}
		if got := cfg.ProcessBypassPermissions(p); !got {
			t.Error("ProcessBypassPermissions() = false, want true")
		}
	})

	t.Run("process bypass override false", func(t *testing.T) {
		p := ProcessConfig{BypassPermissions: &fa}
		if got := cfg.ProcessBypassPermissions(p); got {
			t.Error("ProcessBypassPermissions() = true, want false")
		}
	})

	t.Run("process bypass default", func(t *testing.T) {
		p := ProcessConfig{}
		if got := cfg.ProcessBypassPermissions(p); !got {
			t.Error("ProcessBypassPermissions() = false, want true (from defaults)")
		}
	})

	t.Run("process remote_control override true", func(t *testing.T) {
		p := ProcessConfig{RemoteControl: &tr}
		if got := cfg.ProcessRemoteControl(p); !got {
			t.Error("ProcessRemoteControl() = false, want true")
		}
	})

	t.Run("process remote_control default", func(t *testing.T) {
		p := ProcessConfig{}
		if got := cfg.ProcessRemoteControl(p); got {
			t.Error("ProcessRemoteControl() = true, want false (from defaults)")
		}
	})
}

func TestTaskModel(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
	}

	tests := []struct {
		name      string
		task      TaskConfig
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

func TestProcessMCPConfigPath(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}

	t.Run("default mcp config", func(t *testing.T) {
		p := ProcessConfig{Workspace: "/my/workspace"}
		want := filepath.Join("/my/workspace", "config", "mcp-servers.json")
		if got := cfg.ProcessMCPConfigPath(p); got != want {
			t.Errorf("ProcessMCPConfigPath() = %q, want %q", got, want)
		}
	})

	t.Run("custom relative mcp config", func(t *testing.T) {
		p := ProcessConfig{Workspace: "/my/workspace", MCPConfig: "custom/mcp.json"}
		want := filepath.Join("/my/workspace", "custom/mcp.json")
		if got := cfg.ProcessMCPConfigPath(p); got != want {
			t.Errorf("ProcessMCPConfigPath() = %q, want %q", got, want)
		}
	})

	t.Run("custom absolute mcp config", func(t *testing.T) {
		p := ProcessConfig{Workspace: "/my/workspace", MCPConfig: "/abs/mcp.json"}
		if got := cfg.ProcessMCPConfigPath(p); got != "/abs/mcp.json" {
			t.Errorf("ProcessMCPConfigPath() = %q, want /abs/mcp.json", got)
		}
	})
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")

	tr := true
	cfg := &Config{
		Telegram: TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
		Defaults: DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 10,
		},
		Processes: map[string]ProcessConfig{
			"main": {
				Channels:      []string{"plugin:telegram@claude-plugins-official"},
				RemoteControl: &tr,
				Enabled:       true,
			},
		},
		Tasks: map[string]TaskConfig{
			"heartbeat": {
				Schedule:   "* * * * *",
				PromptFile: "HEARTBEAT.md",
				Enabled:    true,
			},
		},
		HomePath: dir,
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

	if loaded.Telegram.BotToken != cfg.Telegram.BotToken {
		t.Errorf("loaded token = %q, want %q", loaded.Telegram.BotToken, cfg.Telegram.BotToken)
	}
	if len(loaded.Tasks) != 1 {
		t.Errorf("loaded tasks = %d, want 1", len(loaded.Tasks))
	}
	if len(loaded.Processes) != 1 {
		t.Errorf("loaded processes = %d, want 1", len(loaded.Processes))
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

func TestFindConfig(t *testing.T) {
	// Override default home so the fallback doesn't find a real leo.yaml
	origFn := defaultHomeFn
	defaultHomeFn = func() string { return "" }
	defer func() { defaultHomeFn = origFn }()

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
	os.WriteFile(cfgPath, []byte("defaults:\n  model: sonnet\n"), 0644)

	found, err := FindConfig(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("found = %q, want %q", found, cfgPath)
	}
}

func TestFindConfigDefaultHome(t *testing.T) {
	dir := t.TempDir()
	origFn := defaultHomeFn
	defaultHomeFn = func() string { return dir }
	defer func() { defaultHomeFn = origFn }()

	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte("defaults:\n  model: sonnet\n"), 0644)

	// Search from a directory with no leo.yaml
	searchDir := t.TempDir()
	found, err := FindConfig(searchDir)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("found = %q, want %q", found, cfgPath)
	}
}

func TestStatePath(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}
	want := "/home/user/.leo/state"
	if got := cfg.StatePath(); got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
}

func TestTaskWorkspace(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}

	t.Run("explicit workspace", func(t *testing.T) {
		task := TaskConfig{Workspace: "/custom/ws"}
		if got := cfg.TaskWorkspace(task); got != "/custom/ws" {
			t.Errorf("TaskWorkspace() = %q, want /custom/ws", got)
		}
	})

	t.Run("default workspace", func(t *testing.T) {
		task := TaskConfig{}
		want := "/home/user/.leo/workspace"
		if got := cfg.TaskWorkspace(task); got != want {
			t.Errorf("TaskWorkspace() = %q, want %q", got, want)
		}
	})
}

func TestTaskTimeout(t *testing.T) {
	cfg := &Config{Defaults: DefaultsConfig{Model: "sonnet"}}

	t.Run("custom timeout", func(t *testing.T) {
		task := TaskConfig{Timeout: "1h"}
		if got := cfg.TaskTimeout(task); got != time.Hour {
			t.Errorf("TaskTimeout() = %v, want 1h", got)
		}
	})

	t.Run("default timeout", func(t *testing.T) {
		task := TaskConfig{}
		if got := cfg.TaskTimeout(task); got != 30*time.Minute {
			t.Errorf("TaskTimeout() = %v, want 30m", got)
		}
	})

	t.Run("invalid timeout falls back", func(t *testing.T) {
		task := TaskConfig{Timeout: "invalid"}
		if got := cfg.TaskTimeout(task); got != 30*time.Minute {
			t.Errorf("TaskTimeout() = %v, want 30m", got)
		}
	})
}

func TestValidateTaskTimeout(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"bad": {Schedule: "0 * * * *", PromptFile: "t.md", Timeout: "not-a-duration"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !contains(err.Error(), "timeout") {
		t.Errorf("error = %q, want mention of timeout", err.Error())
	}
}

func TestValidateTaskRetries(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"bad": {Schedule: "0 * * * *", PromptFile: "t.md", Retries: -1},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative retries")
	}
}

func TestValidateChannelPattern(t *testing.T) {
	cfg := &Config{
		Processes: map[string]ProcessConfig{
			"bad": {Channels: []string{"$(evil)"}, Enabled: true},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid channel")
	}
	if !contains(err.Error(), "invalid characters") {
		t.Errorf("error = %q, want mention of invalid characters", err.Error())
	}
}

func TestHasMCPServers(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "mcp.json")
		os.WriteFile(f, []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)
		if !HasMCPServers(f) {
			t.Error("should return true for valid config with servers")
		}
	})
	t.Run("empty servers", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "mcp.json")
		os.WriteFile(f, []byte(`{"mcpServers":{}}`), 0644)
		if HasMCPServers(f) {
			t.Error("should return false for empty mcpServers")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if HasMCPServers("/nonexistent/mcp.json") {
			t.Error("should return false for missing file")
		}
	})
}
