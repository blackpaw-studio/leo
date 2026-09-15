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

const fallbackUTF8Locale = "en_US.UTF-8"

// EnsureUTF8Locale returns environ with a UTF-8 locale available to child
// processes when the caller did not choose a locale. tmux uses the first
// non-empty value in LC_ALL, LC_CTYPE, LANG order; an explicit non-UTF-8
// choice is preserved rather than overridden.
func EnsureUTF8Locale(environ []string) []string {
	if _, _, ok := winningLocale(environ); ok {
		return environ
	}

	result := make([]string, len(environ), len(environ)+1)
	copy(result, environ)
	return append(result, "LC_CTYPE="+fallbackUTF8Locale)
}

// UTF8LocaleWarning describes an explicit locale choice that prevents tmux
// from treating its client as UTF-8 capable. An empty result is safe.
func UTF8LocaleWarning(environ []string) string {
	key, value, ok := winningLocale(environ)
	if !ok || isUTF8Locale(value) {
		return ""
	}
	return "tmux client locale " + key + "=" + value + " is not UTF-8; preserving explicit user setting"
}

func winningLocale(environ []string) (string, string, bool) {
	values := make(map[string]string, 3)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, present := values[key]; !present {
			values[key] = value
		}
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := values[key]; value != "" {
			return key, value, true
		}
	}
	return "", "", false
}

func isUTF8Locale(value string) bool {
	value = strings.ToUpper(value)
	return strings.Contains(value, "UTF-8") || strings.Contains(value, "UTF8")
}

// Capture returns a map of environment variables relevant to Leo's daemon
// and cron processes. It ensures common user/Homebrew bin directories are in
// PATH. extraKeys adds caller-specific variables to the capture set; unset
// keys are omitted.
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
