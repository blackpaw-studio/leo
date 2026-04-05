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
	// 0. Check prerequisites
	claude := prereq.CheckClaude()
	if !claude.OK {
		prompt.Err.Println("  claude CLI not found")
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
	prompt.Success.Printf("  claude CLI: %s\n\n", versionStr)

	home, _ := os.UserHomeDir()

	// Try to load existing config for defaults
	var existing *config.Config
	defaultWorkspace := filepath.Join(home, ".leo")
	if cfg, err := config.LoadFromWorkspace(defaultWorkspace); err == nil {
		existing = cfg
		prompt.Info.Printf("  Found existing config at %s\n\n", defaultWorkspace)
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
	botTokenDefault := ""
	chatIDDefault := ""
	groupIDDefault := ""
	if existing != nil {
		botTokenDefault = existing.Telegram.BotToken
		chatIDDefault = existing.Telegram.ChatID
		groupIDDefault = existing.Telegram.GroupID
	}

	var botToken, chatID, groupID string
	var topics map[string]int

	if botTokenDefault != "" {
		masked := botTokenDefault[:8] + "..." + botTokenDefault[len(botTokenDefault)-4:]
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

	// 9. Install cron
	if len(cfg.Tasks) > 0 && prompt.YesNo(reader, "\nInstall cron entries?", true) {
		leoPath, _ := os.Executable()
		if leoPath == "" {
			leoPath = "leo"
		}
		if err := cron.Install(cfg, leoPath); err != nil {
			prompt.Warn.Printf("  Failed to install cron: %v\n", err)
		} else {
			prompt.Success.Println("  Cron entries installed.")
		}
	}

	// 10. Install chat daemon
	daemonStatus, _ := service.DaemonStatus(name)
	if daemonStatus == "not installed" {
		if prompt.YesNo(reader, "\nInstall chat daemon (runs on login)?", true) {
			installDaemon(name, workspace, cfgPath)
		}
	} else {
		prompt.Info.Printf("\n  Chat daemon: %s\n", daemonStatus)
		if prompt.YesNo(reader, "  Reinstall chat daemon?", false) {
			installDaemon(name, workspace, cfgPath)
		}
	}

	// 11. Test
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

func installDaemon(name, workspace, cfgPath string) {
	leoPath, _ := os.Executable()
	if leoPath == "" {
		leoPath = "leo"
	}
	sc := service.ServiceConfig{
		AgentName:  name,
		LeoPath:    leoPath,
		ConfigPath: cfgPath,
		WorkDir:    workspace,
		LogPath:    service.LogPathFor(workspace),
		Env:        captureEnv(),
	}
	if err := service.InstallDaemon(sc); err != nil {
		prompt.Warn.Printf("  Failed to install daemon: %v\n", err)
	} else {
		status, _ := service.DaemonStatus(name)
		prompt.Success.Printf("  Chat daemon installed (%s).\n", status)
		prompt.Info.Printf("  Logs: %s\n", sc.LogPath)
	}
}

func captureEnv() map[string]string {
	env := make(map[string]string)
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_ENTRYPOINT",
		"HOME",
		"PATH",
		"SHELL",
		"USER",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	return env
}
