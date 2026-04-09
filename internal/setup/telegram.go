package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/prompt"
)

var (
	lookPathFn    = exec.LookPath
	statFn        = os.Stat
	writeFileFn   = os.WriteFile
	readFileFn    = os.ReadFile
	mkdirAllFn    = os.MkdirAll
	execCommandFn = exec.Command
)

const superchargedRepo = "https://github.com/k1p1l0/claude-telegram-supercharged.git"

// installTelegramPlugin fully configures the Claude Code telegram channel plugin:
// - Installs the official plugin via claude CLI if not already installed
// - Upgrades to the supercharged fork (forum topics, voice, stickers, etc.)
// - Writes bot token to ~/.claude/channels/telegram/.env
// - Writes access.json with allowlist policy, allowFrom, and groups
// - Writes ~/.claude/settings.json with trustedDirectories, skipDangerousModePermissionPrompt, enabledPlugins
func installTelegramPlugin(botToken, chatID, groupID, workspace string) error {
	home, err := userHomeDirFn()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	// 1. Install the official telegram plugin if not already installed
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "claude-plugins-official", "external_plugins", "telegram")
	if _, err := statFn(pluginDir); os.IsNotExist(err) {
		claudePath, lookErr := lookPathFn("claude")
		if lookErr == nil {
			prompt.Info.Println("  Installing telegram plugin...")
			cmd := execCommandFn(claudePath, "plugin", "install", "telegram@claude-plugins-official")
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

	// 2. Upgrade to supercharged fork (forum topics, voice, stickers, etc.)
	if err := installSuperchargedPlugin(home); err != nil {
		prompt.Warn.Printf("  Supercharged plugin upgrade failed: %v\n", err)
		prompt.Info.Println("  Continuing with official plugin (no forum topic support).")
	}

	// 2. Write bot token to .env
	channelDir := filepath.Join(home, ".claude", "channels", "telegram")
	if err := mkdirAllFn(channelDir, 0750); err != nil {
		return fmt.Errorf("creating channel directory: %w", err)
	}

	if err := appendTelegramEnv("TELEGRAM_BOT_TOKEN", botToken); err != nil {
		return fmt.Errorf("writing .env: %w", err)
	}

	// 3. Write access.json with allowlist policy and groups
	accessPath := filepath.Join(channelDir, "access.json")

	// Read existing access.json to preserve approved peers
	accessDoc := map[string]any{
		"dmPolicy":  "allowlist",
		"allowFrom": []string{},
		"groups":    map[string]any{},
		"pending":   map[string]any{},
	}
	if existingData, readErr := readFileFn(accessPath); readErr == nil {
		if unmarshalErr := json.Unmarshal(existingData, &accessDoc); unmarshalErr != nil {
			return fmt.Errorf("parsing existing access.json: %w", unmarshalErr)
		}
	}

	if chatID != "" {
		allowFrom, ok := accessDoc["allowFrom"].([]any)
		if !ok {
			allowFrom = []any{}
		}
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
		groups, ok := accessDoc["groups"].(map[string]any)
		if !ok || groups == nil {
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
	if err := writeFileFn(accessPath, append(accessData, '\n'), 0600); err != nil {
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
	home, err := userHomeDirFn()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := mkdirAllFn(claudeDir, 0700); err != nil {
		return fmt.Errorf("creating .claude directory: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Read existing settings if present
	existing := make(map[string]any)
	if data, err := readFileFn(settingsPath); err == nil {
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

	return writeFileFn(settingsPath, append(data, '\n'), 0600)
}

// SuperchargedCacheDir returns the path to the cached supercharged plugin clone.
func SuperchargedCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".leo", "cache", "telegram-supercharged"), nil
}

// installSuperchargedPlugin clones or updates the supercharged telegram plugin
// and copies server.ts over the official plugin's cached version.
func installSuperchargedPlugin(home string) error {
	cacheDir, err := SuperchargedCacheDir()
	if err != nil {
		return fmt.Errorf("determining cache directory: %w", err)
	}

	// Clone or pull the supercharged repo
	if _, err := os.Stat(filepath.Join(cacheDir, "server.ts")); os.IsNotExist(err) {
		// Clean up partial clone if directory exists but server.ts is missing
		if _, dirErr := os.Stat(cacheDir); dirErr == nil {
			os.RemoveAll(cacheDir)
		}
		prompt.Info.Println("  Cloning supercharged telegram plugin...")
		if err := os.MkdirAll(filepath.Dir(cacheDir), 0750); err != nil {
			return fmt.Errorf("creating cache directory: %w", err)
		}
		cmd := exec.Command("git", "clone", "--depth", "1", superchargedRepo, cacheDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cloning repo: %w\n%s", err, output)
		}
	} else {
		prompt.Info.Println("  Updating supercharged telegram plugin...")
		cmd := exec.Command("git", "-C", cacheDir, "pull", "--ff-only")
		cmd.CombinedOutput() // best-effort update
	}

	// Find the official plugin cache directory and overwrite server.ts
	return copySuperchargedServer(home, cacheDir)
}

// UpdateSuperchargedPlugin pulls the latest version and re-copies server.ts.
func UpdateSuperchargedPlugin() error {
	cacheDir, err := SuperchargedCacheDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "server.ts")); os.IsNotExist(err) {
		return nil // not installed, skip
	}

	cmd := exec.Command("git", "-C", cacheDir, "pull", "--ff-only")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pulling latest: %w\n%s", err, output)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return copySuperchargedServer(home, cacheDir)
}

// copySuperchargedServer finds the official plugin's cache dir and overwrites server.ts.
func copySuperchargedServer(home, cacheDir string) error {
	pluginCacheBase := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "telegram")
	entries, err := os.ReadDir(pluginCacheBase)
	if err != nil {
		return fmt.Errorf("reading plugin cache: %w", err)
	}

	srcPath := filepath.Join(cacheDir, "server.ts")
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading supercharged server.ts: %w", err)
	}

	copied := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dstPath := filepath.Join(pluginCacheBase, e.Name(), "server.ts")
		if _, err := os.Stat(dstPath); err == nil {
			if err := os.WriteFile(dstPath, srcData, 0644); err != nil {
				return fmt.Errorf("writing %s: %w", dstPath, err)
			}
			copied++
			prompt.Success.Printf("  Upgraded plugin (%s) with supercharged fork.\n", e.Name())
		}
	}

	if copied == 0 {
		return fmt.Errorf("no official plugin cache found at %s", pluginCacheBase)
	}
	return nil
}
