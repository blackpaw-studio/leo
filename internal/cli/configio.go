package cli

import (
	"io"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Testability seams — overridden in tests so confirm-on-reset prompts can be
// exercised without a real TTY.
var (
	processStdin io.Reader = os.Stdin
	processIsTTY           = defaultProcessIsTTY
)

// defaultProcessIsTTY reports whether stdin is an interactive character device.
func defaultProcessIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// saveConfig writes the config to its source path.
func saveConfig(cfg *config.Config) error {
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	return config.Save(cfgPath, cfg)
}

// splitAndTrim splits a comma-separated string and trims whitespace from each
// element, skipping empties. Returns nil for empty input.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
