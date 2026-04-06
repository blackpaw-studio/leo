package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/prompt"
)

// installTelegramPlugin fully configures the Claude Code telegram channel plugin:
// - Installs the plugin via claude CLI if not already installed
// - Writes bot token to ~/.claude/channels/telegram/.env
// - Writes access.json with allowlist policy, allowFrom, and groups
// - Writes ~/.claude/settings.json with trustedDirectories, skipDangerousModePermissionPrompt, enabledPlugins
func installTelegramPlugin(botToken, chatID, groupID, workspace string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	// 1. Install the telegram plugin if not already installed
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "claude-plugins-official", "external_plugins", "telegram")
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		claudePath, lookErr := exec.LookPath("claude")
		if lookErr == nil {
			prompt.Info.Println("  Installing telegram plugin...")
			cmd := exec.Command(claudePath, "plugin", "install", "telegram@claude-plugins-official")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				prompt.Warn.Printf("  Plugin install failed: %v (you can install manually: claude plugin install telegram@claude-plugins-official)\n", err)
			} else {
				prompt.Success.Println("  Telegram plugin installed.")
			}
		}
	} else {
		prompt.Info.Println("  Telegram plugin already installed.")
	}

	// 2. Write bot token to .env
	channelDir := filepath.Join(home, ".claude", "channels", "telegram")
	if err := os.MkdirAll(channelDir, 0750); err != nil {
		return fmt.Errorf("creating channel directory: %w", err)
	}

	envContent := fmt.Sprintf("TELEGRAM_BOT_TOKEN=%s\n", botToken)
	envPath := filepath.Join(channelDir, ".env")
	if err := os.WriteFile(envPath, []byte(envContent), 0600); err != nil {
		return fmt.Errorf("writing .env: %w", err)
	}

	// 3. Write access.json with allowlist policy and groups
	accessPath := filepath.Join(channelDir, "access.json")

	// Read existing access.json to preserve approved peers
	accessDoc := map[string]any{
		"dmPolicy": "allowlist",
		"allowFrom": []string{},
		"groups":    map[string]any{},
		"pending":   map[string]any{},
	}
	if existingData, readErr := os.ReadFile(accessPath); readErr == nil {
		if unmarshalErr := json.Unmarshal(existingData, &accessDoc); unmarshalErr != nil {
			return fmt.Errorf("parsing existing access.json: %w", unmarshalErr)
		}
	}

	if chatID != "" {
		allowFrom, _ := accessDoc["allowFrom"].([]any)
		found := false
		for _, v := range allowFrom {
			if v == chatID {
				found = true
				break
			}
		}
		if !found {
			allowFrom = append(allowFrom, chatID)
		}
		accessDoc["allowFrom"] = allowFrom
	}
	if groupID != "" {
		groups, _ := accessDoc["groups"].(map[string]any)
		if groups == nil {
			groups = make(map[string]any)
		}
		if _, exists := groups[groupID]; !exists {
			groups[groupID] = map[string]any{"requireMention": false}
		}
		accessDoc["groups"] = groups
	}

	accessData, marshalErr := json.MarshalIndent(accessDoc, "", "  ")
	if marshalErr != nil {
		return fmt.Errorf("marshaling access.json: %w", marshalErr)
	}
	if err := os.WriteFile(accessPath, append(accessData, '\n'), 0600); err != nil {
		return fmt.Errorf("writing access.json: %w", err)
	}

	// 4. Write/update ~/.claude/settings.json
	if err := writeClaudeSettings(workspace); err != nil {
		return fmt.Errorf("writing settings.json: %w", err)
	}

	return nil
}

// writeClaudeSettings writes ~/.claude/settings.json with the settings needed
// for headless daemon operation: trustedDirectories, skipDangerousModePermissionPrompt,
// and enabledPlugins for telegram.
func writeClaudeSettings(workspace string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		return fmt.Errorf("creating .claude directory: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Read existing settings if present
	existing := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		if unmarshalErr := json.Unmarshal(data, &existing); unmarshalErr != nil {
			return fmt.Errorf("parsing existing settings.json: %w", unmarshalErr)
		}
	}

	// Ensure trustedDirectories includes the workspace
	trusted, _ := existing["trustedDirectories"].([]any)
	found := false
	for _, d := range trusted {
		if d == workspace {
			found = true
			break
		}
	}
	if !found {
		trusted = append(trusted, workspace)
	}
	existing["trustedDirectories"] = trusted

	// Set skip prompts and enable telegram plugin
	existing["skipDangerousModePermissionPrompt"] = true

	plugins, _ := existing["enabledPlugins"].(map[string]any)
	if plugins == nil {
		plugins = make(map[string]any)
	}
	plugins["telegram@claude-plugins-official"] = true
	existing["enabledPlugins"] = plugins

	// Keep schema if present
	if _, ok := existing["$schema"]; !ok {
		existing["$schema"] = "https://json.schemastore.org/claude-code-settings.json"
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	return os.WriteFile(settingsPath, append(data, '\n'), 0600)
}
