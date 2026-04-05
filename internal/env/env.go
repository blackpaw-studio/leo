package env

import (
	"os"
	"path/filepath"
	"strings"
)

// Capture returns a map of environment variables relevant to Leo's daemon
// and cron processes. It ensures bun and Homebrew are in PATH.
func Capture() map[string]string {
	home, _ := os.UserHomeDir()
	env := make(map[string]string)
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_ENTRYPOINT",
		"HOME",
		"PATH",
		"SHELL",
		"USER",
		"TELEGRAM_BOT_TOKEN",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}

	// Ensure bun and homebrew are in PATH for the daemon
	if path, ok := env["PATH"]; ok && home != "" {
		bunDir := filepath.Join(home, ".bun", "bin")
		if _, err := os.Stat(bunDir); err == nil && !strings.Contains(path, bunDir) {
			env["PATH"] = bunDir + ":" + path
		}
		if !strings.Contains(path, "/opt/homebrew/bin") {
			if _, err := os.Stat("/opt/homebrew/bin"); err == nil {
				env["PATH"] = "/opt/homebrew/bin:" + env["PATH"]
			}
		}
	}

	return env
}
