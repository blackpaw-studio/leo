package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const testYAML = `
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
		return &Config{
			Defaults: DefaultsConfig{
				Model:    "sonnet",
				MaxTurns: 15,
			},
			Tasks: map[string]TaskConfig{
				"heartbeat": {
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					Channels:   []string{"plugin:telegram@claude-plugins-official"},
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

	t.Run("task invalid channel", func(t *testing.T) {
		cfg := validConfig()
		task := cfg.Tasks["heartbeat"]
		task.Channels = []string{"plugin:telegram<bad"}
		cfg.Tasks["heartbeat"] = task
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for invalid channel ID")
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

	t.Run("minimal config with only HomePath is valid", func(t *testing.T) {
		cfg := &Config{HomePath: "/tmp/leo"}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

func TestValidateRejectsRemovedProviders(t *testing.T) {
	validConfig := func() *Config {
		return &Config{
			Defaults: DefaultsConfig{
				Model:    "sonnet",
				MaxTurns: 15,
			},
			Tasks: map[string]TaskConfig{
				"t": {Schedule: "0 * * * *", PromptFile: "HEARTBEAT.md", Enabled: true},
			},
			Templates: map[string]TemplateConfig{
				"x": {},
			},
			Sessions: map[string]SessionConfig{
				"s": {Workspace: "/tmp/leo/workspace"},
			},
			HomePath: "/tmp/leo",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"providers section", func(c *Config) { c.Providers = map[string]any{"corp": map[string]any{}} },
			"providers: this section has been removed — see docs/configuration/harnesses.md"},
		{"defaults.provider", func(c *Config) { c.Defaults.DeprecatedProvider = "corp" },
			"defaults.provider has been removed along with providers — see docs/configuration/harnesses.md"},
		{"tasks.t.provider", func(c *Config) {
			task := c.Tasks["t"]
			task.DeprecatedProvider = "corp"
			c.Tasks["t"] = task
		}, "tasks.t.provider has been removed along with providers — see docs/configuration/harnesses.md"},
		{"templates.x.provider", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedProvider = "corp"
			c.Templates["x"] = tmpl
		}, "templates.x.provider has been removed along with providers — see docs/configuration/harnesses.md"},
		{"sessions.s.provider", func(c *Config) {
			sess := c.Sessions["s"]
			sess.DeprecatedProvider = "corp"
			c.Sessions["s"] = sess
		}, "sessions.s.provider has been removed along with providers — see docs/configuration/harnesses.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !contains(got, tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", got, tt.wantErr)
			}
		})
	}
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

func TestValidateTimezone(t *testing.T) {
	tests := []struct {
		name    string
		tz      string
		wantErr bool
	}{
		{"empty is ok", "", false},
		{"valid iana", "America/New_York", false},
		{"utc", "UTC", false},
		{"injection attempt", "UTC 0 0 * * *", true},
		{"garbage", "not-a-zone", true},
		{"empty slash", "/", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Tasks: map[string]TaskConfig{
					"t": {Schedule: "0 * * * *", PromptFile: "x.md", Timezone: tc.tz},
				},
			}
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil || !contains(err.Error(), "tasks.t.timezone") {
					t.Errorf("Validate() = %v, want error mentioning tasks.t.timezone", err)
				}
			} else if err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
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

func TestIsClientOnly(t *testing.T) {
	hosts := map[string]HostConfig{"alpha": {SSH: "user@host"}}

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "client hosts only",
			cfg:  Config{Client: ClientConfig{Hosts: hosts}},
			want: true,
		},
		{
			name: "no client hosts",
			cfg:  Config{},
			want: false,
		},
		{
			name: "hosts plus local task",
			cfg: Config{
				Client: ClientConfig{Hosts: hosts},
				Tasks:  map[string]TaskConfig{"daily": {}},
			},
			want: false,
		},
		{
			name: "hosts plus template",
			cfg: Config{
				Client:    ClientConfig{Hosts: hosts},
				Templates: map[string]TemplateConfig{"scratch": {}},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsClientOnly(); got != tc.want {
				t.Errorf("IsClientOnly() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultWorkspace(t *testing.T) {
	cfg := &Config{HomePath: "/home/user/.leo"}
	want := "/home/user/.leo/workspace"
	if got := cfg.DefaultWorkspace(); got != want {
		t.Errorf("DefaultWorkspace() = %q, want %q", got, want)
	}
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

// TestTaskModelHarnessCascade verifies model names never leak across
// harnesses: the defaults/built-in fall-through only applies when the task
// runs the same harness as defaults.
func TestTaskModelHarnessCascade(t *testing.T) {
	t.Run("cross-harness task with no model returns empty, not defaults model", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "opus"}, // implicit claude harness
		}
		task := TaskConfig{Harness: "codex"}

		if got := cfg.TaskModel(task); got != "" {
			t.Errorf("TaskModel() = %q, want empty (opus must not leak into a codex task)", got)
		}
	})

	t.Run("cross-harness task with explicit model still wins", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "opus"},
		}
		task := TaskConfig{Harness: "codex", Model: "gpt-5.3-codex"}

		if got := cfg.TaskModel(task); got != "gpt-5.3-codex" {
			t.Errorf("TaskModel() = %q, want %q", got, "gpt-5.3-codex")
		}
	})

	t.Run("same-harness task still falls through to defaults then built-in", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Harness: "codex", Model: "gpt-5.3-codex"},
		}
		task := TaskConfig{Harness: "codex"}

		if got := cfg.TaskModel(task); got != "gpt-5.3-codex" {
			t.Errorf("TaskModel() = %q, want %q", got, "gpt-5.3-codex")
		}

		cfgNoDefaultModel := &Config{Defaults: DefaultsConfig{Harness: "codex"}}
		if got := cfgNoDefaultModel.TaskModel(TaskConfig{Harness: "codex"}); got != DefaultModel {
			t.Errorf("TaskModel() = %q, want built-in default %q", got, DefaultModel)
		}
	})
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")

	cfg := &Config{
		Defaults: DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 10,
		},
		Templates: map[string]TemplateConfig{
			"main": {
				Channels:       []string{"plugin:telegram@claude-plugins-official"},
				HarnessOptions: map[string]any{"remote_control": true},
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

	if len(loaded.Tasks) != 1 {
		t.Errorf("loaded tasks = %d, want 1", len(loaded.Tasks))
	}
	if len(loaded.Templates) != 1 {
		t.Errorf("loaded templates = %d, want 1", len(loaded.Templates))
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

func TestValidateTaskEnvKeys(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"bad": {
				Schedule:   "0 * * * *",
				PromptFile: "t.md",
				Env:        map[string]string{"VALID": "ok", "1INVALID": "bad"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid env key")
	}
	if !contains(err.Error(), "tasks.bad.env key") {
		t.Errorf("error should reference env key, got: %v", err)
	}
}

// TestValidateHarnessKindSupport locks in the harness/kind support matrix:
// codex templates (Plan 4 Task 5) and codex sessions/persistent tasks
// (Plan 4 Task 7 session drivers) all pass SupportsKind now.
func TestValidateHarnessKindSupport(t *testing.T) {
	t.Run("template on codex is valid", func(t *testing.T) {
		cfg := &Config{
			Templates: map[string]TemplateConfig{
				"t": {Harness: "codex"},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("session on codex is now valid", func(t *testing.T) {
		cfg := &Config{
			Sessions: map[string]SessionConfig{
				"s": {Harness: "codex", Workspace: "/tmp/leo/workspace"},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("session on opencode is now valid", func(t *testing.T) {
		cfg := &Config{
			Sessions: map[string]SessionConfig{
				"s": {Harness: "opencode", Workspace: "/tmp/leo/workspace"},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("persistent task on codex is now valid", func(t *testing.T) {
		cfg := &Config{
			Tasks: map[string]TaskConfig{
				"t": {
					Harness:    "codex",
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					Runtime:    "persistent",
					Enabled:    true,
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("persistent task on opencode is now valid", func(t *testing.T) {
		cfg := &Config{
			Tasks: map[string]TaskConfig{
				"t": {
					Harness:    "opencode",
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					Runtime:    "persistent",
					Enabled:    true,
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("scheduled (non-persistent) task on codex is valid", func(t *testing.T) {
		cfg := &Config{
			Tasks: map[string]TaskConfig{
				"t": {
					Harness:    "codex",
					Schedule:   "0 * * * *",
					PromptFile: "HEARTBEAT.md",
					Enabled:    true,
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

func TestValidateChannelPattern(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"bad": {Channels: []string{"$(evil)"}},
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

func TestValidateDevChannelPattern(t *testing.T) {
	cases := []struct {
		name       string
		cfg        *Config
		wantInPath string
	}{
		{
			name: "template",
			cfg: &Config{
				Templates: map[string]TemplateConfig{
					"bad": {DevChannels: []string{"$(evil)"}},
				},
			},
			wantInPath: "templates.bad.dev_channels[0]",
		},
		{
			name: "task",
			cfg: &Config{
				Tasks: map[string]TaskConfig{
					"bad": {
						Schedule:    "@hourly",
						PromptFile:  "prompt.md",
						DevChannels: []string{"$(evil)"},
					},
				},
			},
			wantInPath: "tasks.bad.dev_channels[0]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("expected error for invalid dev_channel")
			}
			if !contains(err.Error(), tc.wantInPath) {
				t.Errorf("error = %q, want path %q", err.Error(), tc.wantInPath)
			}
			if !contains(err.Error(), "invalid characters") {
				t.Errorf("error = %q, want mention of invalid characters", err.Error())
			}
		})
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

func TestValidateWebConfig(t *testing.T) {
	t.Run("valid web config", func(t *testing.T) {
		cfg := &Config{
			Web: WebConfig{Enabled: true, Port: 8370, Bind: "0.0.0.0", AllowedHosts: []string{"10.0.0.1"}},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("invalid port too high", func(t *testing.T) {
		cfg := &Config{
			Web: WebConfig{Port: 70000},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for invalid port")
		}
		if !contains(err.Error(), "web.port") {
			t.Errorf("error = %q, want mention of web.port", err.Error())
		}
	})

	t.Run("invalid port negative", func(t *testing.T) {
		cfg := &Config{
			Web: WebConfig{Port: -1},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for negative port")
		}
	})

	t.Run("invalid bind address", func(t *testing.T) {
		cfg := &Config{
			Web: WebConfig{Bind: "not-an-ip"},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for invalid bind address")
		}
		if !contains(err.Error(), "web.bind") {
			t.Errorf("error = %q, want mention of web.bind", err.Error())
		}
	})

	t.Run("default values", func(t *testing.T) {
		cfg := &Config{}
		if cfg.WebPort() != 8370 {
			t.Errorf("WebPort() = %d, want 8370", cfg.WebPort())
		}
		if cfg.WebBind() != "127.0.0.1" {
			t.Errorf("WebBind() = %q, want \"127.0.0.1\"", cfg.WebBind())
		}
	})

	t.Run("custom values", func(t *testing.T) {
		cfg := &Config{
			Web: WebConfig{Port: 9090, Bind: "127.0.0.1"},
		}
		if cfg.WebPort() != 9090 {
			t.Errorf("WebPort() = %d, want 9090", cfg.WebPort())
		}
		if cfg.WebBind() != "127.0.0.1" {
			t.Errorf("WebBind() = %q, want \"127.0.0.1\"", cfg.WebBind())
		}
	})
}

func TestIsLoopbackBind(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.5", true}, // entire 127.0.0.0/8 is loopback
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"10.0.0.1", false},
		{"not-an-ip", false},
		{"", false},
		// "localhost" is rejected by Config.Validate() before this helper is
		// reached, but pin its behaviour here against future reuse.
		{"localhost", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := IsLoopbackBind(tc.addr); got != tc.want {
				t.Errorf("IsLoopbackBind(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestListPromptFiles(t *testing.T) {
	t.Run("lists md files recursively", func(t *testing.T) {
		ws := t.TempDir()
		os.WriteFile(filepath.Join(ws, "heartbeat.md"), []byte("h"), 0600)
		os.MkdirAll(filepath.Join(ws, "reports"), 0750)
		os.WriteFile(filepath.Join(ws, "reports", "daily.md"), []byte("d"), 0600)
		os.WriteFile(filepath.Join(ws, "not-a-prompt.txt"), []byte("x"), 0600)

		files, err := ListPromptFiles(ws)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("got %d files, want 2: %v", len(files), files)
		}
		// Should contain both .md files but not .txt
		found := map[string]bool{}
		for _, f := range files {
			found[f] = true
		}
		if !found["heartbeat.md"] {
			t.Error("missing heartbeat.md")
		}
		if !found[filepath.Join("reports", "daily.md")] {
			t.Errorf("missing reports/daily.md, got: %v", files)
		}
	})

	t.Run("nonexistent workspace returns nil", func(t *testing.T) {
		files, err := ListPromptFiles("/nonexistent/workspace")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})

	t.Run("empty workspace returns nil", func(t *testing.T) {
		ws := t.TempDir()
		files, err := ListPromptFiles(ws)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})
}

func TestResolvePromptPath(t *testing.T) {
	ws := t.TempDir()

	tests := []struct {
		name        string
		workspace   string
		promptFile  string
		wantErr     bool
		errContains string
	}{
		{"valid relative path", ws, "daily.md", false, ""},
		{"valid nested path", ws, "reports/daily.md", false, ""},
		{"path traversal with dotdot", ws, "../../etc/passwd", true, "escapes workspace"},
		{"empty prompt file", ws, "", true, "empty"},
		// Note: filepath.Join(ws, "/etc/passwd") produces ws+"/etc/passwd" on Unix,
		// which stays within the workspace. This is safe — no test needed.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolvePromptPath(tt.workspace, tt.promptFile)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("expected absolute path, got %q", got)
			}
		})
	}
}

func TestReadPromptFile(t *testing.T) {
	ws := t.TempDir()

	t.Run("file exists", func(t *testing.T) {
		os.WriteFile(filepath.Join(ws, "test.md"), []byte("# Hello\nWorld"), 0644)
		content, err := ReadPromptFile(ws, "test.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "# Hello\nWorld" {
			t.Errorf("content = %q, want %q", content, "# Hello\nWorld")
		}
	})

	t.Run("file not found returns empty", func(t *testing.T) {
		content, err := ReadPromptFile(ws, "nonexistent.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "" {
			t.Errorf("content = %q, want empty string", content)
		}
	})

	t.Run("path traversal blocked", func(t *testing.T) {
		_, err := ReadPromptFile(ws, "../../etc/passwd")
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
		if !contains(err.Error(), "escapes workspace") {
			t.Errorf("error = %q, want mention of escapes workspace", err.Error())
		}
	})
}

func TestWritePromptFile(t *testing.T) {
	t.Run("creates file and parent dirs", func(t *testing.T) {
		ws := t.TempDir()
		err := WritePromptFile(ws, "reports/daily.md", "# Daily Report")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(ws, "reports", "daily.md"))
		if err != nil {
			t.Fatalf("reading written file: %v", err)
		}
		if string(data) != "# Daily Report" {
			t.Errorf("content = %q, want %q", string(data), "# Daily Report")
		}
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		ws := t.TempDir()
		os.WriteFile(filepath.Join(ws, "old.md"), []byte("old content"), 0644)
		err := WritePromptFile(ws, "old.md", "new content")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(ws, "old.md"))
		if string(data) != "new content" {
			t.Errorf("content = %q, want %q", string(data), "new content")
		}
	})

	t.Run("path traversal blocked", func(t *testing.T) {
		ws := t.TempDir()
		err := WritePromptFile(ws, "../../evil.md", "malicious")
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
		if !contains(err.Error(), "escapes workspace") {
			t.Errorf("error = %q, want mention of escapes workspace", err.Error())
		}
	})
}

func TestValidateAddDir(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		wantErr bool
	}{
		// Good paths — must not be rejected.
		{"absolute path", "/Users/me/my-dir", false},
		{"relative path", "subdir/nested", false},
		{"tilde path", "~/code", false},
		{"dotted path", "/opt/app.v2", false},
		{"hyphenated name", "/var/log/my-app_1", false},
		{"path with space", "/Users/me/My Docs", false},
		{"unicode path", "/tmp/café", false},

		// Empty / whitespace-only.
		{"empty", "", true},
		{"whitespace only", "   ", true},

		// Leading dash — could be misread as a flag.
		{"leading dash", "-rf", true},
		{"leading double dash", "--evil", true},

		// Each forbidden shell metacharacter.
		{"semicolon", "/tmp;rm -rf /", true},
		{"ampersand", "/tmp&reboot", true},
		{"pipe", "/tmp|nc evil 1234", true},
		{"dollar", "/tmp/$HOME", true},
		{"backtick", "/tmp/`id`", true},
		{"newline", "/tmp\nrm -rf /", true},
		{"null byte", "/tmp\x00hidden", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAddDir(tt.dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAddDir(%q) error = %v, wantErr %v", tt.dir, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsBadAddDirs(t *testing.T) {
	t.Run("template add_dirs", func(t *testing.T) {
		cfg := &Config{
			Templates: map[string]TemplateConfig{
				"coding": {
					AddDirs: []string{"/tmp/`id`"},
				},
			},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !contains(err.Error(), "templates.coding.add_dirs[0]") {
			t.Errorf("error = %q, want mention of templates.coding.add_dirs[0]", err.Error())
		}
	})
}

func TestValidate_WebAllowedHosts(t *testing.T) {
	cases := []struct {
		name    string
		bind    string
		allowed []string
		wantErr string // substring match; "" means no error
	}{
		{"loopback_no_hosts_ok", "127.0.0.1", nil, ""},
		{"loopback_with_hosts_ok", "127.0.0.1", []string{"leo.local"}, ""},
		{"nonloopback_requires_hosts", "0.0.0.0", nil, "web.allowed_hosts must be set"},
		{"nonloopback_with_hosts_ok", "0.0.0.0", []string{"192.0.2.10"}, ""},
		{"empty_entry_rejected", "127.0.0.1", []string{""}, "web.allowed_hosts[0] must not be empty"},
		{"whitespace_rejected", "127.0.0.1", []string{"bad host"}, "web.allowed_hosts[0] \"bad host\" is not a valid hostname or IP"},
		{"port_in_entry_rejected", "127.0.0.1", []string{"leo.local:8370"}, "must not include a port"},
		{"ipv6_loopback_ok", "127.0.0.1", []string{"::1"}, ""},
		{"ipv6_full_ok", "127.0.0.1", []string{"2001:db8::1"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Web: WebConfig{Enabled: true, Port: 8370, Bind: tc.bind, AllowedHosts: tc.allowed}}
			err := c.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_WebBind_InvalidNoDoubleError(t *testing.T) {
	// An invalid web.bind must produce only the bind error, not also the
	// "web.allowed_hosts must be set" error.
	t.Run("invalid_bind_no_double_error", func(t *testing.T) {
		c := &Config{Web: WebConfig{Enabled: true, Port: 8370, Bind: "not-an-ip"}}
		err := c.Validate()
		if err == nil {
			t.Fatal("expected validation error")
		}
		if strings.Contains(err.Error(), "web.allowed_hosts must be set") {
			t.Fatalf("expected only bind error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "web.bind") {
			t.Fatalf("expected bind error, got: %v", err)
		}
	})
}

func TestSessionConfigParses(t *testing.T) {
	yamlBlob := []byte(`
sessions:
  daily:
    workspace: /tmp/daily
    model: sonnet
    channels:
      - plugin:slack@official
tasks:
  standup:
    runtime: persistent
    session: daily
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels:
      - plugin:slack@official
    queue_max: 3
    lazy: false
`)
	var cfg Config
	if err := yaml.Unmarshal(yamlBlob, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sess, ok := cfg.Sessions["daily"]
	if !ok {
		t.Fatalf("expected sessions.daily")
	}
	if sess.Workspace != "/tmp/daily" || sess.Model != "sonnet" {
		t.Fatalf("session fields wrong: %+v", sess)
	}
	if got := sess.Channels; len(got) != 1 || got[0] != "plugin:slack@official" {
		t.Fatalf("channels wrong: %+v", got)
	}
	task := cfg.Tasks["standup"]
	if task.Runtime != "persistent" || task.Session != "daily" || task.QueueMax != 3 || task.Lazy {
		t.Fatalf("task fields wrong: %+v", task)
	}
}

func TestResolveIdleSuspendCascade(t *testing.T) {
	cfg := &Config{Defaults: DefaultsConfig{IdleSuspendAfter: "24h"}}
	tmpl := TemplateConfig{}

	// defaults only
	if got := cfg.ResolveIdleSuspend(tmpl, ""); got != 24*time.Hour {
		t.Fatalf("defaults: got %v, want 24h", got)
	}
	// template overrides defaults
	tmpl.IdleSuspendAfter = "30m"
	if got := cfg.ResolveIdleSuspend(tmpl, ""); got != 30*time.Minute {
		t.Fatalf("template: got %v, want 30m", got)
	}
	// per-spawn override wins
	if got := cfg.ResolveIdleSuspend(tmpl, "2h"); got != 2*time.Hour {
		t.Fatalf("override: got %v, want 2h", got)
	}
	// unset everywhere => disabled (0)
	if got := (&Config{}).ResolveIdleSuspend(TemplateConfig{}, ""); got != 0 {
		t.Fatalf("unset: got %v, want 0", got)
	}
	// unparseable => disabled (0), not a panic
	if got := (&Config{Defaults: DefaultsConfig{IdleSuspendAfter: "garbage"}}).ResolveIdleSuspend(TemplateConfig{}, ""); got != 0 {
		t.Fatalf("garbage: got %v, want 0", got)
	}
}

func TestValidateRejectsBadIdleSuspend(t *testing.T) {
	cfg := &Config{
		Defaults:  DefaultsConfig{Model: "sonnet", IdleSuspendAfter: "nope"},
		Templates: map[string]TemplateConfig{"t": {IdleSuspendAfter: "5x"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad durations")
	}
	if !strings.Contains(err.Error(), "idle_suspend_after") {
		t.Fatalf("error should mention idle_suspend_after: %v", err)
	}
}
