package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default configuration values used across the codebase.
const (
	DefaultModel             = "sonnet"
	DefaultMaxTurns          = 15
	DefaultTimezone          = "America/New_York"
	DefaultHeartbeatInterval = "30m"
	DefaultHeartbeatStart    = 7
	DefaultHeartbeatEnd      = 22
	DefaultHeartbeatFile     = "HEARTBEAT.md"
)

var validModels = map[string]bool{
	"sonnet": true,
	"opus":   true,
	"haiku":  true,
}

type Config struct {
	Agent     AgentConfig           `yaml:"agent"`
	Telegram  TelegramConfig        `yaml:"telegram"`
	Defaults  DefaultsConfig        `yaml:"defaults"`
	Heartbeat HeartbeatConfig       `yaml:"heartbeat"`
	Tasks     map[string]TaskConfig `yaml:"tasks"`
}

type AgentConfig struct {
	Name      string `yaml:"name"`
	Workspace string `yaml:"workspace"`
	AgentFile string `yaml:"agent_file,omitempty"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
	GroupID  string `yaml:"group_id,omitempty"`
}

type DefaultsConfig struct {
	Model             string `yaml:"model"`
	MaxTurns          int    `yaml:"max_turns"`
	BypassPermissions bool   `yaml:"bypass_permissions,omitempty"`
}

type HeartbeatConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Interval        string `yaml:"interval,omitempty"`          // e.g. "30m", "1h" — default "30m"
	StartHour       *int   `yaml:"start_hour,omitempty"`        // default 7
	EndHour         *int   `yaml:"end_hour,omitempty"`          // default 22
	Timezone        string `yaml:"timezone,omitempty"`
	Model           string `yaml:"model,omitempty"`
	MaxTurns        int    `yaml:"max_turns,omitempty"`
	TopicID         int    `yaml:"topic_id,omitempty"`
	PromptFile      string `yaml:"prompt_file,omitempty"` // default "HEARTBEAT.md"
}

// HeartbeatDefaults returns the heartbeat config with defaults applied.
func (h HeartbeatConfig) WithDefaults() HeartbeatConfig {
	out := h
	if out.Interval == "" {
		out.Interval = DefaultHeartbeatInterval
	}
	if out.StartHour == nil {
		v := DefaultHeartbeatStart
		out.StartHour = &v
	}
	if out.EndHour == nil {
		v := DefaultHeartbeatEnd
		out.EndHour = &v
	}
	if out.PromptFile == "" {
		out.PromptFile = DefaultHeartbeatFile
	}
	return out
}

// Schedule generates a cron expression from the heartbeat config.
// Interval is converted to minute steps within the start/end hour window.
func (h HeartbeatConfig) Schedule() (string, error) {
	hb := h.WithDefaults()

	minutes, err := parseIntervalMinutes(hb.Interval)
	if err != nil {
		return "", fmt.Errorf("heartbeat.interval: %w", err)
	}

	// Build minute field
	var minuteField string
	if minutes >= 60 {
		minuteField = "0"
	} else {
		var mins []string
		for m := 0; m < 60; m += minutes {
			mins = append(mins, strconv.Itoa(m))
		}
		minuteField = strings.Join(mins, ",")
	}

	// Build hour field
	var hourField string
	if minutes >= 60 {
		step := minutes / 60
		hourField = fmt.Sprintf("%d-%d/%d", *hb.StartHour, *hb.EndHour, step)
	} else {
		hourField = fmt.Sprintf("%d-%d", *hb.StartHour, *hb.EndHour)
	}

	return fmt.Sprintf("%s %s * * *", minuteField, hourField), nil
}

// ToTaskConfig converts the heartbeat config to a TaskConfig for the scheduler.
func (h HeartbeatConfig) ToTaskConfig() (TaskConfig, error) {
	hb := h.WithDefaults()
	schedule, err := hb.Schedule()
	if err != nil {
		return TaskConfig{}, err
	}
	return TaskConfig{
		Schedule:   schedule,
		Timezone:   hb.Timezone,
		PromptFile: hb.PromptFile,
		Model:      hb.Model,
		MaxTurns:   hb.MaxTurns,
		TopicID:    hb.TopicID,
		Enabled:    hb.Enabled,
		Silent:     true,
	}, nil
}

// parseIntervalMinutes parses a duration string like "30m", "1h", "2h" into minutes.
func parseIntervalMinutes(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 30, nil
	}
	if strings.HasSuffix(s, "h") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "h"))
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%q is not a valid interval (e.g. 30m, 1h)", s)
		}
		return n * 60, nil
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "m"))
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%q is not a valid interval (e.g. 30m, 1h)", s)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%q is not a valid interval (use e.g. 30m, 1h)", s)
}

type TaskConfig struct {
	Schedule        string `yaml:"schedule"`
	Timezone        string `yaml:"timezone,omitempty"`
	PromptFile      string `yaml:"prompt_file"`
	Model           string `yaml:"model,omitempty"`
	MaxTurns        int    `yaml:"max_turns,omitempty"`
	TopicID         int    `yaml:"topic_id,omitempty"`
	Enabled         bool   `yaml:"enabled"`
	Silent          bool   `yaml:"silent,omitempty"`
}

// Validate checks the config for required fields and valid values.
func (c *Config) Validate() error {
	var errs []string

	if c.Agent.Name == "" {
		errs = append(errs, "agent.name is required")
	}
	if c.Agent.Workspace == "" {
		errs = append(errs, "agent.workspace is required")
	}

	if c.Defaults.Model != "" && !validModels[c.Defaults.Model] {
		errs = append(errs, fmt.Sprintf("defaults.model %q is not valid (use sonnet, opus, or haiku)", c.Defaults.Model))
	}
	if c.Defaults.MaxTurns < 0 {
		errs = append(errs, "defaults.max_turns must not be negative")
	}

	if c.Telegram.BotToken != "" || c.Telegram.ChatID != "" {
		if c.Telegram.BotToken == "" {
			errs = append(errs, "telegram.bot_token is required when telegram is configured")
		}
		if c.Telegram.ChatID == "" && c.Telegram.GroupID == "" {
			errs = append(errs, "telegram.chat_id or telegram.group_id is required when telegram is configured")
		}
	}

	if c.Heartbeat.Enabled {
		if _, err := c.Heartbeat.Schedule(); err != nil {
			errs = append(errs, fmt.Sprintf("heartbeat: %v", err))
		}
		if c.Heartbeat.Model != "" && !validModels[c.Heartbeat.Model] {
			errs = append(errs, fmt.Sprintf("heartbeat.model %q is not valid (use sonnet, opus, or haiku)", c.Heartbeat.Model))
		}
		hb := c.Heartbeat.WithDefaults()
		if *hb.StartHour < 0 || *hb.StartHour > 23 {
			errs = append(errs, "heartbeat.start_hour must be 0-23")
		}
		if *hb.EndHour < 0 || *hb.EndHour > 23 {
			errs = append(errs, "heartbeat.end_hour must be 0-23")
		}
		if *hb.StartHour >= *hb.EndHour {
			errs = append(errs, "heartbeat.start_hour must be less than end_hour")
		}
	}

	for name, task := range c.Tasks {
		if task.Schedule == "" {
			errs = append(errs, fmt.Sprintf("tasks.%s.schedule is required", name))
		} else if err := validateCronExpr(task.Schedule); err != nil {
			errs = append(errs, fmt.Sprintf("tasks.%s.schedule: %v", name, err))
		}
		if task.PromptFile == "" {
			errs = append(errs, fmt.Sprintf("tasks.%s.prompt_file is required", name))
		}
		if task.Model != "" && !validModels[task.Model] {
			errs = append(errs, fmt.Sprintf("tasks.%s.model %q is not valid (use sonnet, opus, or haiku)", name, task.Model))
		}
		if task.MaxTurns < 0 {
			errs = append(errs, fmt.Sprintf("tasks.%s.max_turns must not be negative", name))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// AgentFilePath returns the resolved path to the agent .md file.
func (c *Config) AgentFilePath() string {
	if c.Agent.AgentFile != "" {
		return expandHome(c.Agent.AgentFile)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agents", c.Agent.Name+".md")
}

// MCPConfigPath returns the path to the MCP servers config.
func (c *Config) MCPConfigPath() string {
	return filepath.Join(c.Agent.Workspace, "config", "mcp-servers.json")
}

// TaskModel returns the effective model for a task.
func (c *Config) TaskModel(t TaskConfig) string {
	if t.Model != "" {
		return t.Model
	}
	return c.Defaults.Model
}

// TaskMaxTurns returns the effective max turns for a task.
func (c *Config) TaskMaxTurns(t TaskConfig) int {
	if t.MaxTurns > 0 {
		return t.MaxTurns
	}
	return c.Defaults.MaxTurns
}

// Load reads and parses a leo.yaml config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Agent.Workspace != "" {
		cfg.Agent.Workspace = expandHome(cfg.Agent.Workspace)
	}

	return &cfg, nil
}

// Save writes the config to a YAML file.
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// FindConfig searches for leo.yaml starting from the given directory and walking up.
// If dir is empty, starts from the current working directory.
// DefaultWorkspace returns the default workspace path (~/.leo).
func DefaultWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".leo")
}

func FindConfig(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Walk up from dir looking for leo.yaml
	for d := dir; ; {
		path := filepath.Join(d, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}

		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	// Fall back to default workspace
	if ws := DefaultWorkspace(); ws != "" {
		path := filepath.Join(ws, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("leo.yaml not found")
}

// LoadFromWorkspace loads config from a workspace directory.
func LoadFromWorkspace(workspace string) (*Config, error) {
	return Load(filepath.Join(workspace, "leo.yaml"))
}

// validateCronExpr checks that a cron expression has 5 fields with valid ranges.
func validateCronExpr(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	names := []string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	maxVals := []int{59, 23, 31, 12, 7}

	for i, field := range fields {
		if field == "*" {
			continue
		}
		// Handle step values like */5 or 1-5/2
		parts := strings.SplitN(field, "/", 2)
		if len(parts) == 2 {
			if _, err := strconv.Atoi(parts[1]); err != nil {
				return fmt.Errorf("%s: invalid step %q", names[i], parts[1])
			}
			field = parts[0]
			if field == "*" {
				continue
			}
		}
		// Handle lists like 1,5,10
		for _, item := range strings.Split(field, ",") {
			// Handle ranges like 1-5
			rangeParts := strings.SplitN(item, "-", 2)
			for _, p := range rangeParts {
				n, err := strconv.Atoi(p)
				if err != nil {
					return fmt.Errorf("%s: %q is not a valid number", names[i], p)
				}
				if n < 0 || n > maxVals[i] {
					return fmt.Errorf("%s: %d is out of range (0-%d)", names[i], n, maxVals[i])
				}
			}
		}
	}
	return nil
}

func expandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
