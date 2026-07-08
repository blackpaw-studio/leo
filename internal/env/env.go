package env

import (
	"os"
	"path/filepath"
	"strings"
)

var (
	userHomeDirFn = os.UserHomeDir
	statFn        = os.Stat
)

// Capture returns a map of environment variables relevant to Leo's daemon
// and cron processes. It ensures common user/Homebrew bin directories are in
// PATH. extraKeys adds caller-specific variables (e.g. provider api_key_env
// names) to the capture set; unset keys are omitted.
func Capture(extraKeys ...string) map[string]string {
	home, _ := userHomeDirFn()
	env := make(map[string]string)
	keys := append([]string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_ENTRYPOINT",
		"HOME",
		"PATH",
		"SHELL",
		"USER",
	}, extraKeys...)
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}

	// Ensure common tool directories are in PATH for daemon/cron
	if _, ok := env["PATH"]; ok && home != "" {
		localBinDir := filepath.Join(home, ".local", "bin")
		if _, err := statFn(localBinDir); err == nil && !strings.Contains(env["PATH"], localBinDir) {
			env["PATH"] = localBinDir + ":" + env["PATH"]
		}
		if !strings.Contains(env["PATH"], "/opt/homebrew/bin") {
			if _, err := statFn("/opt/homebrew/bin"); err == nil {
				env["PATH"] = "/opt/homebrew/bin:" + env["PATH"]
			}
		}
	}

	return env
}
