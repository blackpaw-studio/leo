package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Default configuration values used across the codebase.
const (
	DefaultModel    = "sonnet"
	DefaultMaxTurns = 15
)

// channelPattern validates channel plugin identifiers (e.g. "plugin:telegram@claude-plugins-official").
var channelPattern = regexp.MustCompile(`^[a-zA-Z0-9:@._-]+$`)

// envKeyPattern validates environment variable names.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var validModels = map[string]bool{
	"sonnet": true,
	"opus":   true,
	"haiku":  true,
}

var validPermissionModes = map[string]bool{
	"acceptEdits":       true,
	"auto":              true,
	"bypassPermissions": true,
	"default":           true,
	"dontAsk":           true,
	"plan":              true,
}

type Config struct {
	Telegram  TelegramConfig            `yaml:"telegram"`
	Defaults  DefaultsConfig            `yaml:"defaults"`
	Web       WebConfig                 `yaml:"web,omitempty"`
	Client    ClientConfig              `yaml:"client,omitempty"`
	Processes map[string]ProcessConfig  `yaml:"processes"`
	Tasks     map[string]TaskConfig     `yaml:"tasks"`
	Templates map[string]TemplateConfig `yaml:"templates,omitempty"`

	// Set at load time from the config file path, not serialized.
	HomePath string `yaml:"-"`
}

// ClientConfig holds remote-host definitions used when leo is invoked as a
// client to manage agents on a different machine. Empty on servers.
type ClientConfig struct {
	DefaultHost string                `yaml:"default_host,omitempty"`
	Hosts       map[string]HostConfig `yaml:"hosts,omitempty"`
}

// HostConfig describes a remote leo server reachable over SSH.
type HostConfig struct {
	SSH     string   `yaml:"ssh"`                // e.g. "evan@leo.example.com"
	SSHArgs []string `yaml:"ssh_args,omitempty"` // extra args passed to ssh (e.g. ["-p", "2222"])
	// LeoPath overrides the remote leo binary path used when dispatching
	// `leo agent ...` over SSH. Defaults to DefaultRemoteLeoPath. Useful when
	// the remote's non-interactive shell does not have ~/.local/bin on PATH
	// (e.g. PATH export lives in .zshrc rather than .zshenv).
	LeoPath string `yaml:"leo_path,omitempty"`
	// TmuxPath overrides the remote tmux binary path for `agent attach` and
	// `agent logs --follow`. Defaults to DefaultRemoteTmuxPath. On macOS
	// homebrew, /opt/homebrew/bin is typically added in .zprofile which is
	// sourced for login shells but not for `ssh host cmd`, so bare `tmux`
	// may fail to resolve.
	TmuxPath string `yaml:"tmux_path,omitempty"`
}

// DefaultRemoteLeoPath is the default remote binary path used for SSH
// dispatch when HostConfig.LeoPath is unset. Matches install.sh's default
// install dir. The remote shell expands $HOME at execution time.
const DefaultRemoteLeoPath = "$HOME/.local/bin/leo"

// DefaultRemoteTmuxPath is the default remote tmux command used when
// HostConfig.TmuxPath is unset. Bare "tmux" works on most Linux hosts
// where tmux lives in /usr/bin (always on the default non-interactive
// shell PATH). macOS remotes with homebrew typically need to set
// tmux_path: /opt/homebrew/bin/tmux explicitly.
const DefaultRemoteTmuxPath = "tmux"

// RemoteLeoPath returns LeoPath if set, otherwise DefaultRemoteLeoPath.
func (h HostConfig) RemoteLeoPath() string {
	if h.LeoPath != "" {
		return h.LeoPath
	}
	return DefaultRemoteLeoPath
}

// RemoteTmuxPath returns TmuxPath if set, otherwise DefaultRemoteTmuxPath.
// The returned value is a command string that will be appended with tmux
// arguments on the remote (ssh joins its command args with spaces before
// handing off to the remote shell).
func (h HostConfig) RemoteTmuxPath() string {
	if h.TmuxPath != "" {
		return h.TmuxPath
	}
	return DefaultRemoteTmuxPath
}

type WebConfig struct {
	Enabled bool   `yaml:"enabled"`
	Port    int    `yaml:"port,omitempty"`
	Bind    string `yaml:"bind,omitempty"`
}

// WebPort returns the effective web UI port (default 8370).
func (c *Config) WebPort() int {
	if c.Web.Port > 0 {
		return c.Web.Port
	}
	return 8370
}

// WebBind returns the effective web UI bind address (default "0.0.0.0").
func (c *Config) WebBind() string {
	if c.Web.Bind != "" {
		return c.Web.Bind
	}
	return "0.0.0.0"
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
	GroupID  string `yaml:"group_id,omitempty"`
}

type DefaultsConfig struct {
	Model              string   `yaml:"model"`
	MaxTurns           int      `yaml:"max_turns"`
	BypassPermissions  bool     `yaml:"bypass_permissions,omitempty"`
	RemoteControl      bool     `yaml:"remote_control,omitempty"`
	PermissionMode     string   `yaml:"permission_mode,omitempty"`
	AllowedTools       []string `yaml:"allowed_tools,omitempty"`
	DisallowedTools    []string `yaml:"disallowed_tools,omitempty"`
	AppendSystemPrompt string   `yaml:"append_system_prompt,omitempty"`
}

type ProcessConfig struct {
	Workspace          string            `yaml:"workspace,omitempty"`
	Channels           []string          `yaml:"channels,omitempty"`
	Model              string            `yaml:"model,omitempty"`
	MaxTurns           int               `yaml:"max_turns,omitempty"`
	BypassPermissions  *bool             `yaml:"bypass_permissions,omitempty"`
	RemoteControl      *bool             `yaml:"remote_control,omitempty"`
	MCPConfig          string            `yaml:"mcp_config,omitempty"`
	AddDirs            []string          `yaml:"add_dirs,omitempty"`
	Env                map[string]string `yaml:"env,omitempty"`
	Agent              string            `yaml:"agent,omitempty"`
	AllowedTools       []string          `yaml:"allowed_tools,omitempty"`
	DisallowedTools    []string          `yaml:"disallowed_tools,omitempty"`
	AppendSystemPrompt string            `yaml:"append_system_prompt,omitempty"`
	PermissionMode     string            `yaml:"permission_mode,omitempty"`
	Enabled            bool              `yaml:"enabled"`
}

type TaskConfig struct {
	Workspace          string   `yaml:"workspace,omitempty"`
	Schedule           string   `yaml:"schedule"`
	Timezone           string   `yaml:"timezone,omitempty"`
	PromptFile         string   `yaml:"prompt_file"`
	Model              string   `yaml:"model,omitempty"`
	MaxTurns           int      `yaml:"max_turns,omitempty"`
	TopicID            int      `yaml:"topic_id,omitempty"`
	Enabled            bool     `yaml:"enabled"`
	Silent             bool     `yaml:"silent,omitempty"`
	Timeout            string   `yaml:"timeout,omitempty"`         // e.g. "30m", "1h" — default 30m
	Retries            int      `yaml:"retries,omitempty"`         // number of retry attempts on failure, default 0
	NotifyOnFail       bool     `yaml:"notify_on_fail,omitempty"`  // send telegram message on failure
	PermissionMode     string   `yaml:"permission_mode,omitempty"` // acceptEdits, auto, bypassPermissions, default, dontAsk, plan
	AllowedTools       []string `yaml:"allowed_tools,omitempty"`
	DisallowedTools    []string `yaml:"disallowed_tools,omitempty"`
	AppendSystemPrompt string   `yaml:"append_system_prompt,omitempty"`
}

// TemplateConfig defines a reusable blueprint for spawning ephemeral agents.
// Workspace is the base directory — repos are cloned as subdirectories.
type TemplateConfig struct {
	Workspace          string            `yaml:"workspace,omitempty"`
	Channels           []string          `yaml:"channels,omitempty"`
	Model              string            `yaml:"model,omitempty"`
	MaxTurns           int               `yaml:"max_turns,omitempty"`
	RemoteControl      *bool             `yaml:"remote_control,omitempty"`
	MCPConfig          string            `yaml:"mcp_config,omitempty"`
	AddDirs            []string          `yaml:"add_dirs,omitempty"`
	Env                map[string]string `yaml:"env,omitempty"`
	Agent              string            `yaml:"agent,omitempty"`
	AllowedTools       []string          `yaml:"allowed_tools,omitempty"`
	DisallowedTools    []string          `yaml:"disallowed_tools,omitempty"`
	AppendSystemPrompt string            `yaml:"append_system_prompt,omitempty"`
	PermissionMode     string            `yaml:"permission_mode,omitempty"`
}

// DefaultWorkspace returns the default workspace path (HomePath/workspace).
func (c *Config) DefaultWorkspace() string {
	if c.HomePath == "" {
		return ""
	}
	return filepath.Join(c.HomePath, "workspace")
}

// StatePath returns the path to the state directory.
func (c *Config) StatePath() string {
	return filepath.Join(c.HomePath, "state")
}

// ProcessWorkspace returns the effective workspace for a process.
func (c *Config) ProcessWorkspace(p ProcessConfig) string {
	if p.Workspace != "" {
		return p.Workspace
	}
	return c.DefaultWorkspace()
}

// ProcessModel returns the effective model for a process.
func (c *Config) ProcessModel(p ProcessConfig) string {
	if p.Model != "" {
		return p.Model
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// ProcessMaxTurns returns the effective max turns for a process.
func (c *Config) ProcessMaxTurns(p ProcessConfig) int {
	if p.MaxTurns > 0 {
		return p.MaxTurns
	}
	if c.Defaults.MaxTurns > 0 {
		return c.Defaults.MaxTurns
	}
	return DefaultMaxTurns
}

// ProcessBypassPermissions returns the effective bypass_permissions for a process.
func (c *Config) ProcessBypassPermissions(p ProcessConfig) bool {
	if p.BypassPermissions != nil {
		return *p.BypassPermissions
	}
	return c.Defaults.BypassPermissions
}

// ProcessRemoteControl returns the effective remote_control for a process.
func (c *Config) ProcessRemoteControl(p ProcessConfig) bool {
	if p.RemoteControl != nil {
		return *p.RemoteControl
	}
	return c.Defaults.RemoteControl
}

// ProcessMCPConfigPath returns the MCP config path for a process.
// If the process specifies one, it's resolved relative to its workspace.
// Otherwise falls back to <workspace>/config/mcp-servers.json.
func (c *Config) ProcessMCPConfigPath(p ProcessConfig) string {
	ws := c.ProcessWorkspace(p)
	if p.MCPConfig != "" {
		if filepath.IsAbs(p.MCPConfig) {
			return p.MCPConfig
		}
		return filepath.Join(ws, p.MCPConfig)
	}
	return filepath.Join(ws, "config", "mcp-servers.json")
}

// TaskWorkspace returns the effective workspace for a task.
func (c *Config) TaskWorkspace(t TaskConfig) string {
	if t.Workspace != "" {
		return t.Workspace
	}
	return c.DefaultWorkspace()
}

// TaskModel returns the effective model for a task.
func (c *Config) TaskModel(t TaskConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// TaskMaxTurns returns the effective max turns for a task.
func (c *Config) TaskMaxTurns(t TaskConfig) int {
	if t.MaxTurns > 0 {
		return t.MaxTurns
	}
	if c.Defaults.MaxTurns > 0 {
		return c.Defaults.MaxTurns
	}
	return DefaultMaxTurns
}

// TaskMCPConfigPath returns the MCP config path for a task.
func (c *Config) TaskMCPConfigPath(t TaskConfig) string {
	ws := c.TaskWorkspace(t)
	return filepath.Join(ws, "config", "mcp-servers.json")
}

// TaskTimeout returns the effective timeout duration for a task.
func (c *Config) TaskTimeout(t TaskConfig) time.Duration {
	if t.Timeout != "" {
		if d, err := time.ParseDuration(t.Timeout); err == nil {
			return d
		}
	}
	return 30 * time.Minute
}

// Validate checks the config for required fields and valid values.
func (c *Config) Validate() error {
	var errs []string

	if c.Defaults.Model != "" && !validModels[c.Defaults.Model] {
		errs = append(errs, fmt.Sprintf("defaults.model %q is not valid (use sonnet, opus, or haiku)", c.Defaults.Model))
	}
	if c.Defaults.MaxTurns < 0 {
		errs = append(errs, "defaults.max_turns must not be negative")
	}
	if c.Defaults.PermissionMode != "" && !validPermissionModes[c.Defaults.PermissionMode] {
		errs = append(errs, fmt.Sprintf("defaults.permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", c.Defaults.PermissionMode))
	}

	if c.Web.Port != 0 && (c.Web.Port < 1 || c.Web.Port > 65535) {
		errs = append(errs, fmt.Sprintf("web.port %d is out of range (1-65535)", c.Web.Port))
	}
	if c.Web.Bind != "" && net.ParseIP(c.Web.Bind) == nil {
		errs = append(errs, fmt.Sprintf("web.bind %q is not a valid IP address", c.Web.Bind))
	}

	if c.Telegram.BotToken != "" || c.Telegram.ChatID != "" {
		if c.Telegram.BotToken == "" {
			errs = append(errs, "telegram.bot_token is required when telegram is configured")
		}
		if c.Telegram.ChatID == "" && c.Telegram.GroupID == "" {
			errs = append(errs, "telegram.chat_id or telegram.group_id is required when telegram is configured")
		}
	}

	for name, proc := range c.Processes {
		if proc.Model != "" && !validModels[proc.Model] {
			errs = append(errs, fmt.Sprintf("processes.%s.model %q is not valid (use sonnet, opus, or haiku)", name, proc.Model))
		}
		if proc.MaxTurns < 0 {
			errs = append(errs, fmt.Sprintf("processes.%s.max_turns must not be negative", name))
		}
		for i, ch := range proc.Channels {
			if !channelPattern.MatchString(ch) {
				errs = append(errs, fmt.Sprintf("processes.%s.channels[%d] %q contains invalid characters", name, i, ch))
			}
		}
		for k := range proc.Env {
			if !envKeyPattern.MatchString(k) {
				errs = append(errs, fmt.Sprintf("processes.%s.env key %q is not a valid environment variable name", name, k))
			}
		}
		if proc.PermissionMode != "" && !validPermissionModes[proc.PermissionMode] {
			errs = append(errs, fmt.Sprintf("processes.%s.permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", name, proc.PermissionMode))
		}
	}

	for name, tmpl := range c.Templates {
		if tmpl.Model != "" && !validModels[tmpl.Model] {
			errs = append(errs, fmt.Sprintf("templates.%s.model %q is not valid (use sonnet, opus, or haiku)", name, tmpl.Model))
		}
		if tmpl.MaxTurns < 0 {
			errs = append(errs, fmt.Sprintf("templates.%s.max_turns must not be negative", name))
		}
		for i, ch := range tmpl.Channels {
			if !channelPattern.MatchString(ch) {
				errs = append(errs, fmt.Sprintf("templates.%s.channels[%d] %q contains invalid characters", name, i, ch))
			}
		}
		for k := range tmpl.Env {
			if !envKeyPattern.MatchString(k) {
				errs = append(errs, fmt.Sprintf("templates.%s.env key %q is not a valid environment variable name", name, k))
			}
		}
		if tmpl.PermissionMode != "" && !validPermissionModes[tmpl.PermissionMode] {
			errs = append(errs, fmt.Sprintf("templates.%s.permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", name, tmpl.PermissionMode))
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
		if task.Timeout != "" {
			if _, err := time.ParseDuration(task.Timeout); err != nil {
				errs = append(errs, fmt.Sprintf("tasks.%s.timeout %q is not a valid duration", name, task.Timeout))
			}
		}
		if task.Retries < 0 {
			errs = append(errs, fmt.Sprintf("tasks.%s.retries must not be negative", name))
		}
		if task.PermissionMode != "" && !validPermissionModes[task.PermissionMode] {
			errs = append(errs, fmt.Sprintf("tasks.%s.permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", name, task.PermissionMode))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
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

	// Set HomePath from the config file's directory
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}
	cfg.HomePath = filepath.Dir(absPath)

	// Expand ~ in all workspace and path fields
	cfg.expandPaths()

	return &cfg, nil
}

// expandPaths expands ~ in workspace and path fields.
func (c *Config) expandPaths() {
	for name, proc := range c.Processes {
		proc.Workspace = expandHome(proc.Workspace)
		proc.MCPConfig = expandHome(proc.MCPConfig)
		for i, dir := range proc.AddDirs {
			proc.AddDirs[i] = expandHome(dir)
		}
		c.Processes[name] = proc
	}
	for name, task := range c.Tasks {
		task.Workspace = expandHome(task.Workspace)
		c.Tasks[name] = task
	}
	for name, tmpl := range c.Templates {
		tmpl.Workspace = expandHome(tmpl.Workspace)
		tmpl.MCPConfig = expandHome(tmpl.MCPConfig)
		for i, dir := range tmpl.AddDirs {
			tmpl.AddDirs[i] = expandHome(dir)
		}
		c.Templates[name] = tmpl
	}
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

// defaultHomeFn is a testability seam for DefaultHome.
var defaultHomeFn = defaultHomeImpl

// DefaultHome returns the default Leo home path (~/.leo).
func DefaultHome() string {
	return defaultHomeFn()
}

func defaultHomeImpl() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".leo")
}

// FindConfig searches for leo.yaml. Priority:
// 1. Walk up from dir looking for leo.yaml
// 2. Fall back to ~/.leo/leo.yaml
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

	// Fall back to default home
	if home := DefaultHome(); home != "" {
		path := filepath.Join(home, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("leo.yaml not found")
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

// HasMCPServers returns true if the MCP config file exists and contains
// at least one server entry.
func HasMCPServers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return len(cfg.MCPServers) > 0
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// ListPromptFiles returns all .md files in the workspace, as paths relative
// to the workspace root. Returns nil (not an error) if the workspace doesn't exist.
func ListPromptFiles(workspace string) ([]string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	if _, err := os.Stat(absWorkspace); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	err = filepath.Walk(absWorkspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			rel, err := filepath.Rel(absWorkspace, path)
			if err != nil {
				return nil
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing workspace files: %w", err)
	}
	return files, nil
}

// ResolvePromptPath resolves a prompt file path within a task workspace and
// validates that it does not escape the workspace via path traversal.
// Returns the absolute path to the prompt file, or an error.
func ResolvePromptPath(workspace, promptFile string) (string, error) {
	if promptFile == "" {
		return "", fmt.Errorf("prompt_file is empty")
	}
	promptPath := filepath.Join(workspace, promptFile)
	absPrompt, err := filepath.Abs(promptPath)
	if err != nil {
		return "", fmt.Errorf("resolving prompt path: %w", err)
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}
	if !strings.HasPrefix(absPrompt, absWorkspace+string(filepath.Separator)) {
		return "", fmt.Errorf("prompt file %q escapes workspace", promptFile)
	}
	return absPrompt, nil
}

// ReadPromptFile reads a task's prompt file content. Returns empty string
// and nil error if the file does not exist (new file case).
func ReadPromptFile(workspace, promptFile string) (string, error) {
	absPath, err := ResolvePromptPath(workspace, promptFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading prompt file: %w", err)
	}
	return string(data), nil
}

// WritePromptFile writes content to a task's prompt file, creating
// parent directories as needed.
func WritePromptFile(workspace, promptFile, content string) error {
	absPath, err := ResolvePromptPath(workspace, promptFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
		return fmt.Errorf("creating prompt file directory: %w", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("writing prompt file: %w", err)
	}
	return nil
}
