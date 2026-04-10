// Package pluginsync copies the bundled Telegram plugin to the Claude plugin cache.
package pluginsync

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
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
