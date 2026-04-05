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
	// 1. Agent name
	name := prompt.Prompt(reader, "Agent name", "assistant")
	prompt.Info.Printf("  Agent: %s\n\n", name)

	// 2. Workspace directory
	home, _ := os.UserHomeDir()
	defaultWorkspace := filepath.Join(home, ".leo")
	workspace := prompt.Prompt(reader, "Workspace directory", defaultWorkspace)
	workspace = prompt.ExpandHome(workspace)

	// 3. Personality template
	fmt.Println("\nAgent personality:")
	for i, t := range templates.AgentTemplates() {
		fmt.Printf("  %d. %s\n", i+1, t)
	}
	fmt.Printf("  %d. custom (opens $EDITOR)\n", len(templates.AgentTemplates())+1)

	templateChoice := prompt.Prompt(reader, "Choose", "1")
	templateNames := templates.AgentTemplates()
	templateIdx := 0
	if n := prompt.ParseChoice(templateChoice, len(templateNames)+1); n > 0 {
		templateIdx = n - 1
	}

	// 4. User profile
	prompt.Bold.Println("\nUser Profile")
	userName := prompt.Prompt(reader, "Your name", "")
	role := prompt.Prompt(reader, "Your role", "")
	about := prompt.Prompt(reader, "About you (brief)", "")
	preferences := prompt.Prompt(reader, "Communication preferences", "Direct and concise")
	timezone := prompt.Prompt(reader, "Timezone", "America/New_York")

	// 5. Telegram
	prompt.Bold.Println("\nTelegram Setup")
	fmt.Println("Create a bot via @BotFather on Telegram, then paste the token.")
	botToken := prompt.Prompt(reader, "Bot token", "")

	var chatID string
	if botToken != "" {
		fmt.Println("\nSend any message to your bot now. Waiting for chat ID...")
		var err error
		chatID, err = telegram.PollChatID(botToken, 60*time.Second)
		if err != nil {
			prompt.Warn.Printf("  Could not detect chat ID: %v\n", err)
			chatID = prompt.Prompt(reader, "Enter chat ID manually", "")
		} else {
			prompt.Success.Printf("  Detected chat ID: %s\n", chatID)
		}
	}

	groupID := prompt.Prompt(reader, "Forum group ID (optional, press enter to skip)", "")

	var topics map[string]int
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

	// 7. Optional built-in tasks
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

	// Write leo.yaml
	cfgPath := filepath.Join(workspace, "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", cfgPath)

	// Write agent file
	agentDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("creating agent directory: %w", err)
	}

	var agentContent string
	if templateIdx < len(templateNames) {
		var err error
		agentContent, err = templates.RenderAgent(templateNames[templateIdx], templates.AgentData{
			Name:      name,
			UserName:  userName,
			Workspace: workspace,
		})
		if err != nil {
			return fmt.Errorf("rendering agent template: %w", err)
		}
	}

	agentPath := filepath.Join(agentDir, name+".md")
	if err := os.WriteFile(agentPath, []byte(agentContent), 0644); err != nil {
		return fmt.Errorf("writing agent file: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", agentPath)

	// Write USER.md
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
	userPath := filepath.Join(workspace, "USER.md")
	if err := os.WriteFile(userPath, []byte(userContent), 0644); err != nil {
		return fmt.Errorf("writing USER.md: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", userPath)

	// Write HEARTBEAT.md
	heartbeatContent, err := templates.RenderHeartbeat()
	if err != nil {
		return fmt.Errorf("rendering heartbeat: %w", err)
	}
	heartbeatPath := filepath.Join(workspace, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		return fmt.Errorf("writing HEARTBEAT.md: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", heartbeatPath)

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
	_ = os.Remove(memLink) // remove if exists
	if err := os.Symlink(memFile, memLink); err != nil {
		return fmt.Errorf("creating MEMORY.md symlink: %w", err)
	}
	prompt.Info.Printf("  Linked MEMORY.md -> %s\n", memFile)

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

	// 10. Test
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
