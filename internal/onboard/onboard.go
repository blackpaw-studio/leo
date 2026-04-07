package onboard

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/migrate"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/setup"
	"github.com/blackpaw-studio/leo/internal/telegram"
)

var execCommand = exec.Command

// Run executes the unified onboarding flow.
func Run() error {
	reader := prompt.NewReader()

	// 1. Welcome
	fmt.Println()
	prompt.Bold.Println("  Welcome to Leo")
	fmt.Println()
	fmt.Println("  Leo manages Claude Code agents as persistent personal")
	fmt.Println("  assistants with Telegram integration and cron scheduling.")
	fmt.Println()
	fmt.Println("  Let's get you set up.")
	fmt.Println()

	// 2. Prerequisites
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
		fmt.Println("  Then run 'leo onboard' again.")
		return fmt.Errorf("claude CLI not found")
	}

	versionStr := claude.Version
	if versionStr == "" {
		versionStr = "installed"
	}
	prompt.Success.Printf("    claude CLI    ✓ %s\n", versionStr)
	fmt.Println()

	// 3. Detection
	prompt.Bold.Println("Detecting existing installations...")
	fmt.Println()

	ocPath := prereq.FindOpenClaw()
	workspaces := prereq.FindExistingWorkspaces()

	if ocPath != "" {
		prompt.Info.Printf("    OpenClaw      ✓ found at %s\n", ocPath)
	} else {
		fmt.Println("    OpenClaw      — not found")
	}

	if len(workspaces) > 0 {
		prompt.Info.Printf("    Leo workspace ✓ found at %s\n", workspaces[0])
	} else {
		fmt.Println("    Leo workspace — not found")
	}
	fmt.Println()

	// 4. Branch decision
	switch {
	case ocPath != "" && len(workspaces) > 0:
		// Both found — ask what to do
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Fresh setup ��� create a new agent from scratch")
		fmt.Println("  2. Migrate from OpenClaw ��� import your existing agent")
		fmt.Println("  3. Reconfigure — edit an existing Leo workspace")
		choice := prompt.Prompt(reader, "Choose", "1")
		fmt.Println()

		switch prompt.ParseChoice(choice, 3) {
		case 1:
			return setup.RunInteractive(reader)
		case 2:
			return migrate.RunInteractive(reader)
		case 3:
			return reconfigure(reader, workspaces)
		}

	case ocPath != "":
		// Only OpenClaw found
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Migrate from OpenClaw — import your existing agent")
		fmt.Println("  2. Fresh setup — create a new agent from scratch")
		choice := prompt.Prompt(reader, "Choose", "1")
		fmt.Println()

		switch prompt.ParseChoice(choice, 2) {
		case 1:
			return migrate.RunInteractive(reader)
		case 2:
			return setup.RunInteractive(reader)
		}

	case len(workspaces) > 0:
		// Only existing workspace found
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Reconfigure — edit an existing Leo workspace")
		fmt.Println("  2. Fresh setup — create a new agent from scratch")
		choice := prompt.Prompt(reader, "Choose", "1")
		fmt.Println()

		switch prompt.ParseChoice(choice, 2) {
		case 1:
			return reconfigure(reader, workspaces)
		case 2:
			return setup.RunInteractive(reader)
		}

	default:
		// Nothing found — fresh setup
		fmt.Println("No existing installation found. Starting fresh setup.")
		fmt.Println()
		return setup.RunInteractive(reader)
	}

	return nil
}

func reconfigure(reader *bufio.Reader, workspaces []string) error {
	var ws string
	if len(workspaces) == 1 {
		ws = workspaces[0]
	} else {
		fmt.Println("Found multiple workspaces:")
		for i, w := range workspaces {
			fmt.Printf("  %d. %s\n", i+1, w)
		}
		choice := prompt.Prompt(reader, "Choose", "1")
		idx := prompt.ParseChoice(choice, len(workspaces)) - 1
		ws = workspaces[idx]
	}

	cfg, err := config.LoadFromWorkspace(ws)
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", ws, err)
	}

	prompt.Bold.Printf("\nWorkspace: %s\n", ws)
	fmt.Printf("  Agent: %s\n", cfg.Agent.Name)
	if cfg.Telegram.BotToken != "" {
		fmt.Printf("  Telegram: configured\n")
	} else {
		fmt.Printf("  Telegram: not configured\n")
	}
	fmt.Printf("  Tasks: %d\n", len(cfg.Tasks))
	fmt.Println()

	fmt.Println("What would you like to reconfigure?")
	fmt.Println("  1. Telegram settings")
	fmt.Println("  2. Scheduled tasks")
	fmt.Println("  3. Everything (full setup over existing workspace)")
	choice := prompt.Prompt(reader, "Choose", "3")
	fmt.Println()

	switch prompt.ParseChoice(choice, 3) {
	case 1:
		return reconfigureTelegram(reader, cfg, ws)
	case 2:
		return reconfigureTasks(reader, cfg, ws)
	case 3:
		// Full setup will overwrite
		return setup.RunInteractive(reader)
	}

	return nil
}

func reconfigureTelegram(reader *bufio.Reader, cfg *config.Config, ws string) error {
	prompt.Bold.Println("Telegram Setup")
	fmt.Println("Create a bot via @BotFather on Telegram, then paste the token.")

	botToken := prompt.Prompt(reader, "Bot token", cfg.Telegram.BotToken)
	cfg.Telegram.BotToken = botToken

	chatID := prompt.Prompt(reader, "Chat ID", cfg.Telegram.ChatID)
	cfg.Telegram.ChatID = chatID

	groupID := prompt.Prompt(reader, "Forum group ID (optional)", cfg.Telegram.GroupID)
	cfg.Telegram.GroupID = groupID

	cfgPath := ws + "/leo.yaml"
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	prompt.Success.Println("Telegram settings updated.")

	// Test
	if botToken != "" && chatID != "" && prompt.YesNo(reader, "Send test message?", true) {
		effectiveChatID := chatID
		if groupID != "" {
			effectiveChatID = groupID
		}
		if err := telegram.SendMessage(botToken, effectiveChatID, "Hello from Leo! Reconfiguration complete.", 0); err != nil {
			prompt.Warn.Printf("  Test message failed: %v\n", err)
		} else {
			prompt.Success.Println("  Test message sent!")
		}
	}

	return nil
}

func reconfigureTasks(reader *bufio.Reader, cfg *config.Config, ws string) error {
	prompt.Bold.Println("Current tasks:")
	if len(cfg.Tasks) == 0 {
		fmt.Println("  (none)")
	}
	for name, task := range cfg.Tasks {
		status := "disabled"
		if task.Enabled {
			status = "enabled"
		}
		fmt.Printf("  %-25s %-20s %s\n", name, task.Schedule, status)
	}
	fmt.Println()

	// For now, just offer to add the heartbeat task if missing
	if _, ok := cfg.Tasks["heartbeat"]; !ok {
		if prompt.YesNo(reader, "Add heartbeat task?", true) {
			cfg.Tasks["heartbeat"] = config.TaskConfig{
				Schedule:   "0,30 7-22 * * *",
				Timezone:   "America/New_York",
				PromptFile: "HEARTBEAT.md",
				Model:      "sonnet",
				MaxTurns:   10,
				Enabled:    true,
			}
		}
	}

	cfgPath := ws + "/leo.yaml"
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	prompt.Success.Println("Task configuration updated.")

	return nil
}

// SmokeTest runs a quick claude invocation to verify it works.
func SmokeTest() error {
	cmd := execCommand("claude",
		"-p", "Reply with exactly: LEO_SMOKE_OK",
		"--max-turns", "1",
		"--output-format", "text",
	)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("claude smoke test failed: %w", err)
	}

	if !strings.Contains(string(output), "LEO_SMOKE_OK") {
		return fmt.Errorf("unexpected claude output: %s", string(output))
	}

	return nil
}
