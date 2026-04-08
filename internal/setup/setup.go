package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/migrate"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/telegram"
	"github.com/blackpaw-studio/leo/internal/templates"
)

var (
	userHomeDirFn            = os.UserHomeDir
	checkClaudeFn            = prereq.CheckClaude
	checkTmuxFn              = prereq.CheckTmux
	checkBunFn               = prereq.CheckBun
	findOpenClawFn           = prereq.FindOpenClaw
	migrateInteractiveFn     = migrate.RunInteractive
	findExistingWorkspacesFn = prereq.FindExistingWorkspaces
	daemonStatusFn           = service.DaemonStatus
	sendMessageFn            = telegram.SendMessage
	pollChatIDFn             = telegram.PollChatID
	newReaderFn              = prompt.NewReader
)

// Run executes the interactive setup wizard with its own banner.
func Run() error {
	reader := newReaderFn()

	fmt.Println()
	prompt.Bold.Println("  Leo Setup Wizard")
	fmt.Println()

	// Check for existing OpenClaw installation
	if ocPath := findOpenClawFn(); ocPath != "" {
		prompt.Info.Printf("  Found OpenClaw installation at %s\n\n", ocPath)
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Migrate from OpenClaw — import your existing agent")
		fmt.Println("  2. Fresh setup — create a new agent from scratch")
		choice := prompt.Prompt(reader, "Choose", "1")
		fmt.Println()

		if prompt.ParseChoice(choice, 2) == 1 {
			return migrateInteractiveFn(reader)
		}
	}

	return RunInteractive(reader)
}

// RunInteractive executes the setup wizard using the given reader, without printing a banner.
// This allows the onboard command to call it after its own welcome screen.
func RunInteractive(reader *bufio.Reader) error {
	home, err := userHomeDirFn()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	if err := checkPrerequisites(); err != nil {
		return err
	}

	existing, defaultWorkspace := findExistingConfig(home)
	if existing != nil {
		prompt.Info.Printf("  Found existing config at %s/leo.yaml\n\n", existing.Agent.Workspace)
	}

	name, workspace := promptAgentIdentity(reader, existing, defaultWorkspace)

	// If workspace changed, try loading config from the new location too
	if existing == nil && workspace != defaultWorkspace {
		if cfg, err := config.LoadFromWorkspace(workspace); err == nil {
			existing = cfg
			prompt.Info.Printf("  Found existing config at %s\n\n", workspace)
		}
	}

	agentContent, agentDir, agentPath := promptAgentPersonality(reader, home, name, workspace)
	userName, role, about, preferences, timezone := promptUserProfileIfNeeded(reader, workspace)
	botToken, chatID, groupID := promptTelegramConfig(reader, existing)

	cfg := buildConfig(name, workspace, botToken, chatID, groupID, timezone, existing)
	promptHeartbeat(reader, cfg, timezone)

	// Remove legacy heartbeat task if it exists alongside the new config
	delete(cfg.Tasks, "heartbeat")

	prompt.Bold.Println("\nCreating workspace...")
	scaffoldOpts := scaffoldOptions{
		workspace: workspace, home: home, name: name, cfg: cfg,
		agentDir: agentDir, agentPath: agentPath, agentContent: agentContent,
		userPath: filepath.Join(workspace, "USER.md"),
		userName: userName, role: role, about: about,
		preferences: preferences, timezone: timezone,
	}
	if err := scaffoldWorkspace(scaffoldOpts); err != nil {
		return err
	}

	configureTelegramPlugin(botToken, chatID, groupID, workspace)
	cfgPath := filepath.Join(workspace, "leo.yaml")
	promptDaemonInstall(reader, name, workspace, cfgPath, botToken)
	sendTestMessage(reader, botToken, chatID, groupID)

	prompt.Bold.Printf("\nSetup complete! Workspace: %s\n", workspace)
	fmt.Println("\nNext steps:")
	fmt.Printf("  leo chat                 # Start interactive session\n")
	fmt.Printf("  leo run heartbeat        # Test heartbeat task\n")
	fmt.Printf("  leo task list            # View configured tasks\n")

	return nil
}

func promptAgentIdentity(reader *bufio.Reader, existing *config.Config, defaultWorkspace string) (name, workspace string) {
	nameDefault := "assistant"
	if existing != nil {
		nameDefault = existing.Agent.Name
	}
	name = prompt.Prompt(reader, "Agent name", nameDefault)
	prompt.Info.Printf("  Agent: %s\n\n", name)

	wsDefault := defaultWorkspace
	if existing != nil {
		wsDefault = existing.Agent.Workspace
	}
	workspace = prompt.Prompt(reader, "Workspace directory", wsDefault)
	workspace = prompt.ExpandHome(workspace)
	return
}

func promptAgentPersonality(reader *bufio.Reader, home, name, workspace string) (agentContent, agentDir, agentPath string) {
	agentDir = filepath.Join(home, ".claude", "agents")
	agentPath = filepath.Join(agentDir, name+".md")

	if _, err := os.Stat(agentPath); err == nil {
		prompt.Info.Printf("  Agent file exists: %s\n", agentPath)
		if prompt.YesNo(reader, "  Overwrite agent personality?", false) {
			agentContent = chooseAgentTemplate(reader, name, "", workspace)
		}
	} else {
		agentContent = chooseAgentTemplate(reader, name, "", workspace)
	}
	return
}

func promptUserProfileIfNeeded(reader *bufio.Reader, workspace string) (userName, role, about, preferences, timezone string) {
	userPath := filepath.Join(workspace, "USER.md")

	if _, err := os.Stat(userPath); err == nil {
		prompt.Info.Printf("  USER.md exists: %s\n", userPath)
		if prompt.YesNo(reader, "  Overwrite user profile?", false) {
			userName, role, about, preferences, timezone = promptUserProfile(reader)
		}
	} else {
		userName, role, about, preferences, timezone = promptUserProfile(reader)
	}

	if timezone == "" {
		timezone = config.DefaultTimezone
	}
	return
}

func buildConfig(name, workspace, botToken, chatID, groupID, timezone string, existing *config.Config) *config.Config {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      name,
			Workspace: workspace,
		},
		Telegram: config.TelegramConfig{
			BotToken: botToken,
			ChatID:   chatID,
			GroupID:  groupID,
		},
		Defaults: config.DefaultsConfig{
			Model:    config.DefaultModel,
			MaxTurns: config.DefaultMaxTurns,
		},
		Tasks: make(map[string]config.TaskConfig),
	}

	if existing != nil {
		cfg.Defaults = existing.Defaults
		for k, v := range existing.Tasks {
			cfg.Tasks[k] = v
		}
	}
	return cfg
}

func promptHeartbeat(reader *bufio.Reader, cfg *config.Config, timezone string) {
	if !cfg.Heartbeat.Enabled {
		prompt.Bold.Println("\nHeartbeat")
		if prompt.YesNo(reader, "Enable heartbeat? (checks in every 30 minutes during waking hours)", true) {
			cfg.Heartbeat = config.HeartbeatConfig{
				Enabled:  true,
				Interval: config.DefaultHeartbeatInterval,
				Timezone: timezone,
				Model:    config.DefaultModel,
				MaxTurns: 10,
			}
		}
	} else {
		prompt.Info.Println("\n  Heartbeat already configured.")
	}
}

func configureTelegramPlugin(botToken, chatID, groupID, workspace string) {
	if botToken != "" {
		prompt.Bold.Println("\nConfiguring Telegram channel plugin...")
		if err := installTelegramPlugin(botToken, chatID, groupID, workspace); err != nil {
			prompt.Warn.Printf("  Failed to configure Telegram plugin: %v\n", err)
		} else {
			prompt.Success.Println("  Telegram channel plugin configured.")
		}
	} else {
		if err := writeClaudeSettings(workspace); err != nil {
			prompt.Warn.Printf("  Failed to write Claude settings: %v\n", err)
		}
	}
}

func promptDaemonInstall(reader *bufio.Reader, name, workspace, cfgPath, botToken string) {
	fmt.Println()
	daemonStatus, daemonErr := daemonStatusFn(name)
	if daemonErr != nil {
		prompt.Warn.Printf("  Could not check daemon status: %v\n", daemonErr)
	}
	if daemonStatus == "not installed" || daemonErr != nil {
		if prompt.YesNo(reader, "Install chat daemon (runs on login)?", true) {
			fmt.Println("  Installing chat daemon...")
			installDaemon(name, workspace, cfgPath, botToken)
		}
	} else {
		prompt.Info.Printf("  Chat daemon: %s\n", daemonStatus)
		if prompt.YesNo(reader, "  Reinstall chat daemon?", false) {
			fmt.Println("  Installing chat daemon...")
			installDaemon(name, workspace, cfgPath, botToken)
		}
	}
}

func sendTestMessage(reader *bufio.Reader, botToken, chatID, groupID string) {
	if botToken != "" && chatID != "" && prompt.YesNo(reader, "\nSend test Telegram message?", true) {
		effectiveChatID := chatID
		if groupID != "" {
			effectiveChatID = groupID
		}
		if err := sendMessageFn(botToken, effectiveChatID, "Hello from Leo! Setup complete.", 0); err != nil {
			prompt.Warn.Printf("  Test message failed: %v\n", err)
		} else {
			prompt.Success.Println("  Test message sent!")
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

	if checkBunFn() {
		prompt.Success.Println("    bun           ✓ installed")
	} else {
		prompt.Err.Println("    bun           ✗ not found")
		fmt.Println()
		fmt.Println("  bun is required for the Telegram plugin. Install it:")
		fmt.Println()
		fmt.Println("    curl -fsSL https://bun.sh/install | bash")
		fmt.Println()
		fmt.Println("  Then run 'leo setup' again.")
		return fmt.Errorf("bun not found")
	}

	fmt.Println()
	return nil
}

func findExistingConfig(home string) (*config.Config, string) {
	defaultWorkspace := filepath.Join(home, ".leo")

	if cfg, err := config.LoadFromWorkspace(defaultWorkspace); err == nil {
		return cfg, defaultWorkspace
	}

	for _, ws := range findExistingWorkspacesFn() {
		if cfg, err := config.LoadFromWorkspace(ws); err == nil {
			return cfg, ws
		}
	}

	return nil, defaultWorkspace
}

type scaffoldOptions struct {
	workspace, home, name                          string
	cfg                                            *config.Config
	agentDir, agentPath, agentContent              string
	userPath, userName, role, about, preferences   string
	timezone                                       string
}

func scaffoldWorkspace(opts scaffoldOptions) error {
	workspace := opts.workspace
	name := opts.name
	cfg := opts.cfg
	dirs := []string{
		workspace,
		filepath.Join(workspace, "daily"),
		filepath.Join(workspace, "reports"),
		filepath.Join(workspace, "state"),
		filepath.Join(workspace, "config"),
		filepath.Join(workspace, "scripts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// Write leo.yaml (always — merges existing + new settings)
	cfgPath := filepath.Join(workspace, "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", cfgPath)

	// Write agent file (only if we have new content)
	if opts.agentContent != "" {
		if err := os.MkdirAll(opts.agentDir, 0750); err != nil {
			return fmt.Errorf("creating agent directory: %w", err)
		}
		if err := os.WriteFile(opts.agentPath, []byte(opts.agentContent), 0644); err != nil {
			return fmt.Errorf("writing agent file: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", opts.agentPath)
	}

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

	// Write HEARTBEAT.md (only if missing)
	heartbeatPath := filepath.Join(workspace, "HEARTBEAT.md")
	if _, err := os.Stat(heartbeatPath); os.IsNotExist(err) {
		heartbeatContent, err := templates.RenderHeartbeat()
		if err != nil {
			return fmt.Errorf("rendering heartbeat: %w", err)
		}
		if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
			return fmt.Errorf("writing HEARTBEAT.md: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", heartbeatPath)
	}

	// Write CLAUDE.md (only if missing)
	claudeMDPath := filepath.Join(workspace, "CLAUDE.md")
	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		claudeContent, err := templates.RenderClaudeWorkspace(templates.AgentData{
			Name:      name,
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

func chooseAgentTemplate(reader *bufio.Reader, name, userName, workspace string) string {
	fmt.Println("\nAgent personality:")
	templateNames := templates.AgentTemplates()
	for i, t := range templateNames {
		fmt.Printf("  %d. %s\n", i+1, t)
	}
	fmt.Printf("  %d. custom (opens $EDITOR)\n", len(templateNames)+1)

	templateChoice := prompt.Prompt(reader, "Choose", "1")
	templateIdx := 0
	if n := prompt.ParseChoice(templateChoice, len(templateNames)+1); n > 0 {
		templateIdx = n - 1
	}

	if templateIdx < len(templateNames) {
		content, err := templates.RenderAgent(templateNames[templateIdx], templates.AgentData{
			Name:      name,
			UserName:  userName,
			Workspace: workspace,
		})
		if err != nil {
			prompt.Warn.Printf("  Failed to render template: %v\n", err)
			return ""
		}
		return content
	}
	return ""
}

func promptUserProfile(reader *bufio.Reader) (userName, role, about, preferences, timezone string) {
	prompt.Bold.Println("\nUser Profile")
	userName = prompt.Prompt(reader, "Your name", "")
	role = prompt.Prompt(reader, "Your role", "")
	about = prompt.Prompt(reader, "About you (brief)", "")
	preferences = prompt.Prompt(reader, "Communication preferences", "Direct and concise")
	timezone = prompt.Prompt(reader, "Timezone", config.DefaultTimezone)
	return
}

func promptTelegramConfig(reader *bufio.Reader, existing *config.Config) (botToken, chatID, groupID string) {
	botTokenDefault := ""
	chatIDDefault := ""
	groupIDDefault := ""
	if existing != nil {
		botTokenDefault = existing.Telegram.BotToken
		chatIDDefault = existing.Telegram.ChatID
		groupIDDefault = existing.Telegram.GroupID
	}

	if botTokenDefault != "" {
		masked := botTokenDefault
		if len(botTokenDefault) > 12 {
			masked = botTokenDefault[:8] + "..." + botTokenDefault[len(botTokenDefault)-4:]
		}
		prompt.Info.Printf("  Telegram bot token: %s\n", masked)
		if chatIDDefault != "" {
			prompt.Info.Printf("  Chat ID: %s\n", chatIDDefault)
		}
		if groupIDDefault != "" {
			prompt.Info.Printf("  Group ID: %s\n", groupIDDefault)
		}

		if prompt.YesNo(reader, "  Reconfigure Telegram?", false) {
			botToken, chatID, groupID = promptTelegram(reader, botTokenDefault, chatIDDefault, groupIDDefault)
		} else {
			botToken = botTokenDefault
			chatID = chatIDDefault
			groupID = groupIDDefault
		}
	} else {
		botToken, chatID, groupID = promptTelegram(reader, "", "", "")
	}
	return
}

func promptTelegram(reader *bufio.Reader, tokenDefault, chatDefault, groupDefault string) (botToken, chatID, groupID string) {
	prompt.Bold.Println("\nTelegram Setup")
	fmt.Println("Create a bot via @BotFather on Telegram, then paste the token.")
	botToken = prompt.Prompt(reader, "Bot token", tokenDefault)

	if botToken != "" && chatDefault == "" {
		fmt.Println("\nSend any message to your bot now. Waiting for chat ID...")
		var err error
		chatID, err = pollChatIDFn(botToken, 60*time.Second)
		if err != nil {
			prompt.Warn.Printf("  Could not detect chat ID: %v\n", err)
			chatID = prompt.Prompt(reader, "Enter chat ID manually", "")
		} else {
			prompt.Success.Printf("  Detected chat ID: %s\n", chatID)
		}
	} else {
		chatID = prompt.Prompt(reader, "Chat ID", chatDefault)
	}

	groupID = prompt.Prompt(reader, "Forum group ID (optional, press enter to skip)", groupDefault)

	if groupID != "" {
		fmt.Println("  Topic IDs are discovered automatically. Run 'leo telegram topics' after setup.")
	}
	return
}
