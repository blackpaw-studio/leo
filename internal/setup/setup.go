package setup

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/templates"
)

var (
	userHomeDirFn  = os.UserHomeDir
	checkClaudeFn  = prereq.CheckClaude
	checkTmuxFn    = prereq.CheckTmux
	daemonStatusFn = service.DaemonStatus
	newReaderFn    = prompt.NewReader
	sshExecFn      = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, name, args...)
	}
)

// Run executes the interactive setup wizard with its own banner.
func Run() error {
	reader := newReaderFn()

	fmt.Println()
	prompt.Bold.Println("  Leo Setup Wizard")
	fmt.Println()

	return RunInteractive(reader)
}

// RunInteractive executes the setup wizard using the given reader, without printing a banner.
// This allows the onboard command to call it after its own welcome screen.
func RunInteractive(reader *bufio.Reader) error {
	home, err := userHomeDirFn()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	existing, defaultHome := findExistingConfig(home)
	if existing != nil {
		prompt.Info.Printf("  Found existing config at %s/leo.yaml\n\n", existing.HomePath)
	}

	if promptSetupMode(reader, existing) == "client" {
		return runClientSetup(reader, defaultHome, existing)
	}

	if err := checkPrerequisites(); err != nil {
		return err
	}

	workspace := promptWorkspace(reader, existing, filepath.Join(defaultHome, "workspace"))

	// Derive the leo home from the workspace (workspace is <home>/workspace)
	leoHome := filepath.Dir(workspace)

	if err := checkWorkspaceWritable(workspace); err != nil {
		return err
	}

	userName, role, about, preferences, timezone := promptUserProfileIfNeeded(reader, workspace)

	cfg := buildConfig(workspace, existing)
	cfg.HomePath = leoHome

	// Summary
	prompt.Bold.Println("\nConfiguration Summary")
	fmt.Printf("  Leo home:  %s\n", leoHome)
	fmt.Printf("  Workspace: %s\n", workspace)
	if userName != "" {
		fmt.Printf("  User:      %s\n", userName)
	}
	fmt.Printf("  Processes: %d\n", len(cfg.Processes))
	fmt.Printf("  Tasks:     %d\n", len(cfg.Tasks))
	fmt.Println()

	if !prompt.YesNo(reader, "Proceed with setup?", true) {
		return fmt.Errorf("setup cancelled")
	}

	prompt.Bold.Println("\nCreating workspace...")
	scaffoldOpts := scaffoldOptions{
		workspace: workspace, home: home, leoHome: leoHome, cfg: cfg,
		userPath: filepath.Join(workspace, "USER.md"),
		userName: userName, role: role, about: about,
		preferences: preferences, timezone: timezone,
	}
	if err := scaffoldWorkspace(scaffoldOpts); err != nil {
		return err
	}

	cfgPath := filepath.Join(leoHome, "leo.yaml")
	promptDaemonInstall(reader, leoHome, cfgPath)

	prompt.Bold.Printf("\nSetup complete! Leo home: %s\n", leoHome)
	fmt.Println()
	fmt.Println("To surface messages via a Claude Code channel plugin (Telegram, Slack, webhook,")
	fmt.Println("etc.), install the plugin separately and add its ID to your process's channels:")
	fmt.Println()
	fmt.Println("  claude plugin install telegram@claude-plugins-official")
	fmt.Println("  # then edit leo.yaml → processes.assistant.channels:")
	fmt.Println("  #   - plugin:telegram@claude-plugins-official")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  leo service              # Start interactive session\n")
	fmt.Printf("  leo task list            # View configured tasks\n")

	return nil
}

func promptWorkspace(reader *bufio.Reader, existing *config.Config, defaultWorkspace string) string {
	wsDefault := defaultWorkspace
	if existing != nil {
		wsDefault = existing.DefaultWorkspace()
	}
	workspace := prompt.Prompt(reader, "Workspace directory", wsDefault)
	workspace = prompt.ExpandHome(workspace)
	if absPath, err := filepath.Abs(workspace); err == nil {
		workspace = absPath
	}
	return workspace
}

func promptUserProfileIfNeeded(reader *bufio.Reader, workspace string) (userName, role, about, preferences, timezone string) {
	userPath := filepath.Join(workspace, "USER.md")

	var defaults templates.UserProfileData
	if _, err := os.Stat(userPath); err == nil {
		defaults = parseUserProfile(userPath)
		label := defaults.UserName
		if label == "" {
			label = "custom format — will be preserved"
		}
		prompt.Info.Printf("  USER.md exists: %s (%s)\n", userPath, label)
		if !prompt.YesNo(reader, "  Update user profile?", false) {
			// Return empty strings as a sentinel so the existing file is left untouched.
			return "", "", "", "", ""
		}
	}

	userName, role, about, preferences, timezone = promptUserProfile(reader, defaults)

	if timezone == "" {
		timezone = "America/New_York"
	}
	return
}

// parseUserProfile reads an existing USER.md and extracts field values.
func parseUserProfile(path string) templates.UserProfileData {
	data, err := os.ReadFile(path)
	if err != nil {
		return templates.UserProfileData{}
	}

	var result templates.UserProfileData
	lines := strings.Split(string(data), "\n")
	var currentField string
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "## Name":
			currentField = "name"
		case "## Role":
			currentField = "role"
		case "## About":
			currentField = "about"
		case "## Preferences":
			currentField = "preferences"
		case "## Timezone":
			currentField = "timezone"
		case "# User Profile", "":
			continue
		default:
			val := strings.TrimSpace(line)
			switch currentField {
			case "name":
				result.UserName = val
			case "role":
				result.Role = val
			case "about":
				result.About = val
			case "preferences":
				result.Preferences = val
			case "timezone":
				result.Timezone = val
			}
			currentField = ""
		}
	}
	return result
}

func buildConfig(workspace string, existing *config.Config) *config.Config {
	tr := true
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:    config.DefaultModel,
			MaxTurns: config.DefaultMaxTurns,
		},
		Processes: map[string]config.ProcessConfig{
			"assistant": {
				Workspace:     workspace,
				RemoteControl: &tr,
				Enabled:       true,
			},
		},
		Tasks: make(map[string]config.TaskConfig),
	}

	if existing != nil {
		cfg.Defaults = existing.Defaults
		for k, v := range existing.Processes {
			cfg.Processes[k] = v
		}
		for k, v := range existing.Tasks {
			cfg.Tasks[k] = v
		}
	}
	return cfg
}

func promptDaemonInstall(reader *bufio.Reader, workspace, cfgPath string) {
	fmt.Println()
	daemonStatus, daemonErr := daemonStatusFn()
	if daemonErr != nil {
		prompt.Warn.Printf("  Could not check daemon status: %v\n", daemonErr)
	}
	if daemonStatus == "not installed" || daemonErr != nil {
		if prompt.YesNo(reader, "Install chat daemon (runs on login)?", true) {
			fmt.Println("  Installing chat daemon...")
			installDaemon(workspace, cfgPath)
		}
	} else {
		prompt.Info.Printf("  Chat daemon: %s\n", daemonStatus)
		if prompt.YesNo(reader, "  Reinstall chat daemon?", false) {
			fmt.Println("  Installing chat daemon...")
			installDaemon(workspace, cfgPath)
		}
	}
}

func checkPrerequisites() error {
	prompt.Bold.Println("Checking prerequisites...")
	fmt.Println()

	claude := checkClaudeFn()
	if !claude.OK {
		prompt.Err.Println("    claude CLI    ✗ not found")
		fmt.Println()
		fmt.Println("  Claude Code CLI is required. Install it:")
		fmt.Println()
		fmt.Println("    brew install claude-code")
		fmt.Println("    — or —")
		fmt.Println("    npm install -g @anthropic-ai/claude-code")
		fmt.Println()
		fmt.Println("  Then run 'leo setup' again.")
		return fmt.Errorf("claude CLI not found")
	}
	versionStr := claude.Version
	if versionStr == "" {
		versionStr = "installed"
	}
	prompt.Success.Printf("    claude CLI    ✓ %s\n", versionStr)

	if checkTmuxFn() {
		prompt.Success.Println("    tmux          ✓ installed")
	} else {
		prompt.Err.Println("    tmux          ✗ not found")
		fmt.Println()
		fmt.Println("  tmux is required for the chat daemon. Install it:")
		fmt.Println()
		fmt.Println("    brew install tmux")
		fmt.Println()
		fmt.Println("  Then run 'leo setup' again.")
		return fmt.Errorf("tmux not found")
	}

	fmt.Println()
	return nil
}

func checkWorkspaceWritable(workspace string) error {
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return fmt.Errorf("cannot create workspace directory %s: %w", workspace, err)
	}
	testFile := filepath.Join(workspace, ".leo-write-test")
	if err := os.WriteFile(testFile, []byte(""), 0600); err != nil {
		return fmt.Errorf("workspace directory %s is not writable: %w", workspace, err)
	}
	os.Remove(testFile)
	return nil
}

func findExistingConfig(home string) (*config.Config, string) {
	defaultHome := filepath.Join(home, ".leo")

	cfgPath := filepath.Join(defaultHome, "leo.yaml")
	if cfg, err := config.Load(cfgPath); err == nil {
		return cfg, defaultHome
	}

	return nil, defaultHome
}

type scaffoldOptions struct {
	workspace, home, leoHome                     string
	cfg                                          *config.Config
	userPath, userName, role, about, preferences string
	timezone                                     string
}

func scaffoldWorkspace(opts scaffoldOptions) error {
	workspace := opts.workspace
	leoHome := opts.leoHome
	cfg := opts.cfg
	dirs := []string{
		leoHome,
		filepath.Join(leoHome, "state"),
		workspace,
		filepath.Join(workspace, "daily"),
		filepath.Join(workspace, "reports"),
		filepath.Join(workspace, "config"),
		filepath.Join(workspace, "scripts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// Write leo.yaml to leo home (always — merges existing + new settings)
	cfgPath := filepath.Join(leoHome, "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", cfgPath)

	// Write USER.md (only if we collected new profile data)
	if opts.userName != "" {
		userContent, err := templates.RenderUserProfile(templates.UserProfileData{
			UserName:    opts.userName,
			Role:        opts.role,
			About:       opts.about,
			Preferences: opts.preferences,
			Timezone:    opts.timezone,
		})
		if err != nil {
			return fmt.Errorf("rendering user profile: %w", err)
		}
		if err := os.WriteFile(opts.userPath, []byte(userContent), 0644); err != nil {
			return fmt.Errorf("writing USER.md: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", opts.userPath)
	}

	// Write CLAUDE.md (only if missing)
	claudeMDPath := filepath.Join(workspace, "CLAUDE.md")
	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		claudeContent, err := templates.RenderClaudeWorkspace(templates.AgentData{
			Workspace: workspace,
		})
		if err != nil {
			return fmt.Errorf("rendering CLAUDE.md: %w", err)
		}
		if err := os.WriteFile(claudeMDPath, []byte(claudeContent), 0644); err != nil {
			return fmt.Errorf("writing CLAUDE.md: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", claudeMDPath)
	}

	// Write skill files (only if missing)
	skillsDir := filepath.Join(workspace, "skills")
	if err := os.MkdirAll(skillsDir, 0750); err != nil {
		return fmt.Errorf("creating skills directory: %w", err)
	}
	for _, skillName := range templates.SkillFiles() {
		skillPath := filepath.Join(skillsDir, skillName)
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			content, err := templates.ReadSkill(skillName)
			if err != nil {
				return fmt.Errorf("reading skill template %s: %w", skillName, err)
			}
			if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing skill %s: %w", skillName, err)
			}
			prompt.Info.Printf("  Wrote %s\n", skillPath)
		}
	}

	// Write empty MCP config if not exists
	mcpPath := filepath.Join(workspace, "config", "mcp-servers.json")
	if _, err := os.Stat(mcpPath); os.IsNotExist(err) {
		if err := os.WriteFile(mcpPath, []byte("{}\n"), 0644); err != nil {
			return fmt.Errorf("writing mcp-servers.json: %w", err)
		}
	}

	return nil
}

// promptSetupMode asks whether Leo will run on this machine (server) or
// drive a remote Leo host over SSH (client). The default is "server" for a
// fresh install and "client" only when the existing config is strictly
// client-only (hosts defined, no processes/tasks/templates). Hybrid
// configs (hosts + any local primitives) default to server so a re-run
// can't silently clobber a server install.
func promptSetupMode(reader *bufio.Reader, existing *config.Config) string {
	defaultClient := existing != nil && existing.IsClientOnly()

	prompt.Bold.Println("Where will Leo run?")
	fmt.Println("  1) On this machine (server)   — supervise agents locally")
	fmt.Println("  2) On another machine (client) — drive a remote host over SSH")
	fmt.Println()

	defaultLabel := "1"
	if defaultClient {
		defaultLabel = "2"
	}
	answer := prompt.Prompt(reader, "Choice", defaultLabel)
	fmt.Println()
	if strings.TrimSpace(answer) == "2" {
		return "client"
	}
	return "server"
}

// runClientSetup writes a client-only leo.yaml (a single host in
// client.hosts + default_host). It does not scaffold a workspace, check
// server prerequisites, or install the daemon.
func runClientSetup(reader *bufio.Reader, leoHome string, existing *config.Config) error {
	prompt.Bold.Println("Client setup")
	fmt.Println("  Leo will use SSH to run commands on the remote host.")
	fmt.Println("  The remote host must already have leo installed.")
	fmt.Println()

	nickname, host := promptClientHost(reader, existing)

	if prompt.YesNo(reader, "Test SSH connectivity now?", true) {
		if err := testSSHConnectivity(host); err != nil {
			prompt.Warn.Printf("  ✗ SSH test failed: %v\n", err)
			if !prompt.YesNo(reader, "  Continue anyway?", true) {
				return fmt.Errorf("setup cancelled")
			}
		} else {
			prompt.Success.Printf("  ✓ Reached %s\n", nickname)
		}
	}

	cfg := buildClientConfig(existing, nickname, host, reader)
	cfg.HomePath = leoHome

	// Summary
	prompt.Bold.Println("\nConfiguration Summary")
	fmt.Printf("  Leo home:     %s\n", leoHome)
	fmt.Printf("  Default host: %s  (%s)\n", cfg.Client.DefaultHost, host.SSH)
	fmt.Println()

	if !prompt.YesNo(reader, "Proceed with setup?", true) {
		return fmt.Errorf("setup cancelled")
	}

	for _, dir := range []string{leoHome, filepath.Join(leoHome, "state")} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	cfgPath := filepath.Join(leoHome, "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", cfgPath)

	prompt.Bold.Printf("\nSetup complete! Leo home: %s\n", leoHome)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  leo agent list               # list agents on the remote host")
	fmt.Println("  leo agent spawn <template>   # spawn remotely")
	fmt.Println("  leo --host <name> ...        # target a different host for one command")
	return nil
}

// promptClientHost gathers the fields needed for a single client.hosts entry.
// Returns the nickname (map key) and the populated HostConfig.
func promptClientHost(reader *bufio.Reader, existing *config.Config) (string, config.HostConfig) {
	var (
		nicknameDefault string
		sshDefault      string
		portDefault     int
		leoPathDefault  string
	)
	if existing != nil && existing.Client.DefaultHost != "" {
		if h, ok := existing.Client.Hosts[existing.Client.DefaultHost]; ok {
			nicknameDefault = existing.Client.DefaultHost
			sshDefault = h.SSH
			leoPathDefault = h.LeoPath
			for i := 0; i < len(h.SSHArgs)-1; i++ {
				if h.SSHArgs[i] == "-p" {
					if p, err := strconv.Atoi(h.SSHArgs[i+1]); err == nil {
						portDefault = p
					} else {
						prompt.Warn.Printf("  Ignoring non-numeric port %q from existing config\n", h.SSHArgs[i+1])
					}
					break
				}
			}
		}
	}

	nickname := strings.TrimSpace(prompt.Prompt(reader, "Host nickname", nicknameDefault))
	for nickname == "" {
		prompt.Warn.Println("  A nickname is required (used as the map key in client.hosts).")
		nickname = strings.TrimSpace(prompt.Prompt(reader, "Host nickname", nicknameDefault))
	}

	sshTarget := strings.TrimSpace(prompt.Prompt(reader, "SSH target (user@host or ssh config alias)", sshDefault))
	for sshTarget == "" {
		prompt.Warn.Println("  An SSH target is required (e.g. evan@leo.example.com).")
		sshTarget = strings.TrimSpace(prompt.Prompt(reader, "SSH target", sshDefault))
	}

	port := prompt.PromptInt(reader, "SSH port (blank for default)", portDefault)

	leoPath := strings.TrimSpace(prompt.Prompt(reader, "Remote leo binary path", leoPathDefault))

	host := config.HostConfig{SSH: sshTarget, LeoPath: leoPath}
	if port > 0 {
		host.SSHArgs = []string{"-p", strconv.Itoa(port)}
	}
	return nickname, host
}

// testSSHConnectivity runs `ssh [ssh_args...] <target> <leo_path> version`
// with a 10-second context timeout and returns a non-nil error describing
// any failure. BatchMode=yes and ConnectTimeout=8 are prepended so the
// probe fails fast instead of blocking on host-key confirmation, password,
// or 2FA prompts during setup.
func testSSHConnectivity(host config.HostConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8"}
	args = append(args, host.SSHArgs...)
	args = append(args, host.SSH, host.RemoteLeoPath(), "version")
	cmd := sshExecFn(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return nil
}

// buildClientConfig merges the new host/nickname into existing config (if
// any) and returns a fresh *config.Config. Existing map fields
// (Processes/Tasks/Templates/Client.Hosts) are deep-copied so writes to
// the returned config never alias back into the caller's existing value.
func buildClientConfig(existing *config.Config, nickname string, host config.HostConfig, reader *bufio.Reader) *config.Config {
	cfg := &config.Config{}
	if existing != nil {
		*cfg = *existing
		cfg.Processes = maps.Clone(existing.Processes)
		cfg.Tasks = maps.Clone(existing.Tasks)
		cfg.Templates = maps.Clone(existing.Templates)
		cfg.Client.Hosts = maps.Clone(existing.Client.Hosts)
	}
	if cfg.Client.Hosts == nil {
		cfg.Client.Hosts = map[string]config.HostConfig{}
	}
	cfg.Client.Hosts[nickname] = host

	switch {
	case cfg.Client.DefaultHost == "":
		cfg.Client.DefaultHost = nickname
	case cfg.Client.DefaultHost != nickname:
		label := fmt.Sprintf("  Replace default host %q with %q?", cfg.Client.DefaultHost, nickname)
		if prompt.YesNo(reader, label, true) {
			cfg.Client.DefaultHost = nickname
		}
	}
	return cfg
}

func promptUserProfile(reader *bufio.Reader, defaults templates.UserProfileData) (userName, role, about, preferences, timezone string) {
	prompt.Bold.Println("\nUser Profile")
	userName = prompt.Prompt(reader, "Your name", defaults.UserName)
	role = prompt.Prompt(reader, "Your role", defaults.Role)
	about = prompt.Prompt(reader, "About you (brief)", defaults.About)
	prefsDefault := defaults.Preferences
	if prefsDefault == "" {
		prefsDefault = "Direct and concise"
	}
	preferences = prompt.Prompt(reader, "Communication preferences", prefsDefault)
	tzDefault := defaults.Timezone
	if tzDefault == "" {
		tzDefault = "America/New_York"
	}
	timezone = prompt.Prompt(reader, "Timezone", tzDefault)
	return
}
