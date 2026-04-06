package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/migrate"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/telegram"
	"github.com/blackpaw-studio/leo/internal/templates"
)

// Run executes the interactive setup wizard with its own banner.
func Run() error {
	reader := prompt.NewReader()

	fmt.Println()
	prompt.Bold.Println("  Leo Setup Wizard")
	fmt.Println()

	// Check for existing OpenClaw installation
	if ocPath := prereq.FindOpenClaw(); ocPath != "" {
		prompt.Info.Printf("  Found OpenClaw installation at %s\n\n", ocPath)
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Migrate from OpenClaw — import your existing agent")
		fmt.Println("  2. Fresh setup — create a new agent from scratch")
		choice := prompt.Prompt(reader, "Choose", "1")
		fmt.Println()

		if prompt.ParseChoice(choice, 2) == 1 {
			return migrate.RunInteractive(reader)
		}
	}

	return RunInteractive(reader)
}

// RunInteractive executes the setup wizard using the given reader, without printing a banner.
// This allows the onboard command to call it after its own welcome screen.
func RunInteractive(reader *bufio.Reader) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	// 0. Check prerequisites
	if err := checkPrerequisites(); err != nil {
		return err
	}

	// Try to load existing config
	existing, defaultWorkspace := findExistingConfig(home)

	if existing != nil {
		prompt.Info.Printf("  Found existing config at %s/leo.yaml\n\n", existing.Agent.Workspace)
	}

	// 1. Agent name
	nameDefault := "assistant"
	if existing != nil {
		nameDefault = existing.Agent.Name
	}
	name := prompt.Prompt(reader, "Agent name", nameDefault)
	prompt.Info.Printf("  Agent: %s\n\n", name)

	// 2. Workspace directory
	wsDefault := defaultWorkspace
	if existing != nil {
		wsDefault = existing.Agent.Workspace
	}
	workspace := prompt.Prompt(reader, "Workspace directory", wsDefault)
	workspace = prompt.ExpandHome(workspace)

	// If workspace changed, try loading config from the new location too
	if existing == nil && workspace != defaultWorkspace {
		if cfg, err := config.LoadFromWorkspace(workspace); err == nil {
			existing = cfg
			prompt.Info.Printf("  Found existing config at %s\n\n", workspace)
		}
	}

	// 3. Personality template
	agentDir := filepath.Join(home, ".claude", "agents")
	agentPath := filepath.Join(agentDir, name+".md")
	var agentContent string

	if _, err := os.Stat(agentPath); err == nil {
		prompt.Info.Printf("  Agent file exists: %s\n", agentPath)
		if prompt.YesNo(reader, "  Overwrite agent personality?", false) {
			agentContent = chooseAgentTemplate(reader, name, "", workspace)
		}
	} else {
		agentContent = chooseAgentTemplate(reader, name, "", workspace)
	}

	// 4. User profile
	userPath := filepath.Join(workspace, "USER.md")
	var userName, role, about, preferences, timezone string

	if _, err := os.Stat(userPath); err == nil {
		prompt.Info.Printf("  USER.md exists: %s\n", userPath)
		if prompt.YesNo(reader, "  Overwrite user profile?", false) {
			userName, role, about, preferences, timezone = promptUserProfile(reader)
		}
	} else {
		userName, role, about, preferences, timezone = promptUserProfile(reader)
	}

	if timezone == "" {
		timezone = "America/New_York"
	}

	// 5. Telegram
	botToken, chatID, groupID, topics := promptTelegramConfig(reader, existing)

	// 6. Build config
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      name,
			Workspace: workspace,
		},
		Telegram: config.TelegramConfig{
			BotToken: botToken,
			ChatID:   chatID,
			GroupID:  groupID,
			Topics:   topics,
		},
		Defaults: config.DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
		},
		Tasks: make(map[string]config.TaskConfig),
	}

	// Preserve existing tasks and defaults
	if existing != nil {
		cfg.Defaults = existing.Defaults
		for k, v := range existing.Tasks {
			cfg.Tasks[k] = v
		}
	}

	// 7. Optional built-in tasks
	if _, hasHeartbeat := cfg.Tasks["heartbeat"]; !hasHeartbeat {
		prompt.Bold.Println("\nScheduled Tasks")
		if prompt.YesNo(reader, "Add heartbeat task?", true) {
			cfg.Tasks["heartbeat"] = config.TaskConfig{
				Schedule:   "0,30 7-22 * * *",
				Timezone:   timezone,
				PromptFile: "HEARTBEAT.md",
				Model:      "sonnet",
				MaxTurns:   10,
				Topic:      "alerts",
				Enabled:    true,
			}
		}
	} else {
		prompt.Info.Println("\n  Heartbeat task already configured.")
	}

	// 8. Create workspace and write files
	prompt.Bold.Println("\nCreating workspace...")

	if err := scaffoldWorkspace(workspace, home, name, cfg, agentDir, agentPath, agentContent, userPath, userName, role, about, preferences, timezone); err != nil {
		return err
	}

	// 9. Configure Telegram channel plugin for Claude Code
	if botToken != "" {
		prompt.Bold.Println("\nConfiguring Telegram channel plugin...")
		if err := installTelegramPlugin(botToken, chatID, groupID, workspace); err != nil {
			prompt.Warn.Printf("  Failed to configure Telegram plugin: %v\n", err)
		} else {
			prompt.Success.Println("  Telegram channel plugin configured.")
		}
	} else {
		// Still write settings.json for trusted dirs even without telegram
		if err := writeClaudeSettings(workspace); err != nil {
			prompt.Warn.Printf("  Failed to write Claude settings: %v\n", err)
		}
	}

	// 10. Install cron
	cfgPath := filepath.Join(workspace, "leo.yaml")
	if len(cfg.Tasks) > 0 {
		cronInstalled := cron.Installed(name)
		if cronInstalled {
			prompt.Info.Println("\n  Cron entries already installed.")
			if prompt.YesNo(reader, "  Reinstall cron entries?", false) {
				installCron(cfg)
			}
		} else {
			if prompt.YesNo(reader, "\nInstall cron entries?", true) {
				installCron(cfg)
			}
		}
	}

	// 11. Install chat daemon
	fmt.Println()
	daemonStatus, daemonErr := service.DaemonStatus(name)
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

	// 12. Test
	if botToken != "" && chatID != "" && prompt.YesNo(reader, "\nSend test Telegram message?", true) {
		effectiveChatID := chatID
		if groupID != "" {
			effectiveChatID = groupID
		}
		if err := telegram.SendMessage(botToken, effectiveChatID, "Hello from Leo! Setup complete.", 0); err != nil {
			prompt.Warn.Printf("  Test message failed: %v\n", err)
		} else {
			prompt.Success.Println("  Test message sent!")
		}
	}

	prompt.Bold.Printf("\nSetup complete! Workspace: %s\n", workspace)
	fmt.Println("\nNext steps:")
	fmt.Printf("  leo chat                 # Start interactive session\n")
	fmt.Printf("  leo run heartbeat        # Test heartbeat task\n")
	fmt.Printf("  leo task list            # View configured tasks\n")

	return nil
}

func checkPrerequisites() error {
	prompt.Bold.Println("Checking prerequisites...")
	fmt.Println()

	claude := prereq.CheckClaude()
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

	if prereq.CheckTmux() {
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

	if prereq.CheckBun() {
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

	for _, ws := range prereq.FindExistingWorkspaces() {
		if cfg, err := config.LoadFromWorkspace(ws); err == nil {
			return cfg, ws
		}
	}

	return nil, defaultWorkspace
}

func scaffoldWorkspace(workspace, home, name string, cfg *config.Config, agentDir, agentPath, agentContent, userPath, userName, role, about, preferences, timezone string) error {
	dirs := []string{
		workspace,
		filepath.Join(workspace, "daily"),
		filepath.Join(workspace, "reports"),
		filepath.Join(workspace, "state"),
		filepath.Join(workspace, "config"),
		filepath.Join(workspace, "scripts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
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
	if agentContent != "" {
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			return fmt.Errorf("creating agent directory: %w", err)
		}
		if err := os.WriteFile(agentPath, []byte(agentContent), 0644); err != nil {
			return fmt.Errorf("writing agent file: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", agentPath)
	}

	// Write USER.md (only if we collected new profile data)
	if userName != "" {
		userContent, err := templates.RenderUserProfile(templates.UserProfileData{
			UserName:    userName,
			Role:        role,
			About:       about,
			Preferences: preferences,
			Timezone:    timezone,
		})
		if err != nil {
			return fmt.Errorf("rendering user profile: %w", err)
		}
		if err := os.WriteFile(userPath, []byte(userContent), 0644); err != nil {
			return fmt.Errorf("writing USER.md: %w", err)
		}
		prompt.Info.Printf("  Wrote %s\n", userPath)
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
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
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

	// Create agent memory directory and symlink
	memDir := filepath.Join(home, ".claude", "agent-memory", name)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}

	memFile := filepath.Join(memDir, "MEMORY.md")
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		if err := os.WriteFile(memFile, []byte("# Memory\n\n"), 0644); err != nil {
			return fmt.Errorf("creating MEMORY.md: %w", err)
		}
	}

	memLink := filepath.Join(workspace, "MEMORY.md")
	if _, err := os.Lstat(memLink); os.IsNotExist(err) {
		if err := os.Symlink(memFile, memLink); err != nil {
			return fmt.Errorf("creating MEMORY.md symlink: %w", err)
		}
		prompt.Info.Printf("  Linked MEMORY.md -> %s\n", memFile)
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
	timezone = prompt.Prompt(reader, "Timezone", "America/New_York")
	return
}

func promptTelegramConfig(reader *bufio.Reader, existing *config.Config) (botToken, chatID, groupID string, topics map[string]int) {
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
			botToken, chatID, groupID, topics = promptTelegram(reader, botTokenDefault, chatIDDefault, groupIDDefault)
		} else {
			botToken = botTokenDefault
			chatID = chatIDDefault
			groupID = groupIDDefault
			if existing != nil {
				topics = existing.Telegram.Topics
			}
		}
	} else {
		botToken, chatID, groupID, topics = promptTelegram(reader, "", "", "")
	}
	return
}

func promptTelegram(reader *bufio.Reader, tokenDefault, chatDefault, groupDefault string) (botToken, chatID, groupID string, topics map[string]int) {
	prompt.Bold.Println("\nTelegram Setup")
	fmt.Println("Create a bot via @BotFather on Telegram, then paste the token.")
	botToken = prompt.Prompt(reader, "Bot token", tokenDefault)

	if botToken != "" && chatDefault == "" {
		fmt.Println("\nSend any message to your bot now. Waiting for chat ID...")
		var err error
		chatID, err = telegram.PollChatID(botToken, 60*time.Second)
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
		topics = make(map[string]int)
		fmt.Println("Enter topic IDs (press enter to skip each):")
		if id := prompt.PromptInt(reader, "  alerts topic ID", 0); id > 0 {
			topics["alerts"] = id
		}
		if id := prompt.PromptInt(reader, "  construction topic ID", 0); id > 0 {
			topics["construction"] = id
		}
		if id := prompt.PromptInt(reader, "  news topic ID", 0); id > 0 {
			topics["news"] = id
		}
	}
	return
}
