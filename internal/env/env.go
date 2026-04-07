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

	// Ensure common tool directories are in PATH for daemon/cron
	if path, ok := env["PATH"]; ok && home != "" {
		bunDir := filepath.Join(home, ".bun", "bin")
		if _, err := os.Stat(bunDir); err == nil && !strings.Contains(path, bunDir) {
			env["PATH"] = bunDir + ":" + path
		}
		localBinDir := filepath.Join(home, ".local", "bin")
		if _, err := os.Stat(localBinDir); err == nil && !strings.Contains(env["PATH"], localBinDir) {
			env["PATH"] = localBinDir + ":" + env["PATH"]
		}
		if !strings.Contains(env["PATH"], "/opt/homebrew/bin") {
			if _, err := os.Stat("/opt/homebrew/bin"); err == nil {
				env["PATH"] = "/opt/homebrew/bin:" + env["PATH"]
			}
		}
	}

	return env
}
