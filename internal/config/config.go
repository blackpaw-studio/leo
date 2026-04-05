package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agent    AgentConfig    `yaml:"agent"`
	Telegram TelegramConfig `yaml:"telegram"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Tasks    map[string]TaskConfig `yaml:"tasks"`
}

type AgentConfig struct {
	Name      string `yaml:"name"`
	Workspace string `yaml:"workspace"`
	AgentFile string `yaml:"agent_file,omitempty"`
}

type TelegramConfig struct {
	BotToken string            `yaml:"bot_token"`
	ChatID   string            `yaml:"chat_id"`
	GroupID  string            `yaml:"group_id,omitempty"`
	Topics   map[string]int    `yaml:"topics,omitempty"`
}

type DefaultsConfig struct {
	Model             string `yaml:"model"`
	MaxTurns          int    `yaml:"max_turns"`
	BypassPermissions bool   `yaml:"bypass_permissions,omitempty"`
}

type TaskConfig struct {
	Schedule   string `yaml:"schedule"`
	Timezone   string `yaml:"timezone,omitempty"`
	PromptFile string `yaml:"prompt_file"`
	Model      string `yaml:"model,omitempty"`
	MaxTurns   int    `yaml:"max_turns,omitempty"`
	Topic      string `yaml:"topic,omitempty"`
	Enabled    bool   `yaml:"enabled"`
	Silent     bool   `yaml:"silent,omitempty"`
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

// TopicID returns the message_thread_id for a topic name, or 0 if not found.
func (c *Config) TopicID(topicName string) int {
	if topicName == "" || c.Telegram.Topics == nil {
		return 0
	}
	return c.Telegram.Topics[topicName]
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

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// FindConfig searches for leo.yaml starting from the given directory and walking up.
// If dir is empty, starts from the current working directory.
func FindConfig(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
	}

	for {
		path := filepath.Join(dir, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("leo.yaml not found")
}

// LoadFromWorkspace loads config from a workspace directory.
func LoadFromWorkspace(workspace string) (*Config, error) {
	return Load(filepath.Join(workspace, "leo.yaml"))
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
