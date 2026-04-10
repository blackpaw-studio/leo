// Package pluginsync copies the bundled Telegram plugin to the Claude plugin cache.
package pluginsync

import (
	"embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed telegram/server.ts telegram/package.json
var pluginFiles embed.FS

// SyncTelegramPlugin copies the bundled Telegram plugin files to the Claude
// plugin cache, overwriting the official plugin. This ensures our forked
// version with Leo control commands (/stop, etc.) is used.
func SyncTelegramPlugin() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home dir: %w", err)
	}

	// Find the installed plugin directory
	cacheBase := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "telegram")
	entries, err := os.ReadDir(cacheBase)
	if err != nil {
		return fmt.Errorf("reading plugin cache: %w", err)
	}

	// Sync to all version directories (usually just one)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		destDir := filepath.Join(cacheBase, entry.Name())

		for _, name := range []string{"server.ts", "package.json"} {
			data, err := pluginFiles.ReadFile("telegram/" + name)
			if err != nil {
				return fmt.Errorf("reading embedded %s: %w", name, err)
			}
			dest := filepath.Join(destDir, name)
			if err := os.WriteFile(dest, data, 0644); err != nil {
				return fmt.Errorf("writing %s: %w", dest, err)
			}
		}
	}

	return nil
}

// RegisterBotCommands sets the Telegram bot's command menu via the Bot API.
// This makes commands show up when users type "/" in the chat.
// Commands are registered for both group chats (default scope) and private chats.
func RegisterBotCommands(botToken string) error {
	if botToken == "" {
		return nil
	}

	commands := `"commands":[` +
		`{"command":"stop","description":"Interrupt the current Claude operation"},` +
		`{"command":"clear","description":"Clear conversation context"},` +
		`{"command":"compact","description":"Compact conversation context"},` +
		`{"command":"agent","description":"Spawn a coding agent (/agent <template> <repo>)"},` +
		`{"command":"agents","description":"List running agents"},` +
		`{"command":"tasks","description":"List and manage scheduled tasks"},` +
		`{"command":"status","description":"Show bot connection status"},` +
		`{"command":"help","description":"Show available commands"},` +
		`{"command":"start","description":"Start a conversation"}` +
		`]`

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", botToken)

	// Register for default scope (group chats)
	resp, err := http.Post(apiURL, "application/json", strings.NewReader(`{`+commands+`}`)) // #nosec G107 -- URL constructed from config bot token
	if err != nil {
		return fmt.Errorf("setting bot commands: %w", err)
	}
	resp.Body.Close()

	// Register for private chats
	resp, err = http.Post(apiURL, "application/json", strings.NewReader(`{`+commands+`,"scope":{"type":"all_private_chats"}}`)) // #nosec G107
	if err != nil {
		return fmt.Errorf("setting bot commands (private): %w", err)
	}
	resp.Body.Close()

	return nil
}
