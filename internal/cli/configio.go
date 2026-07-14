package cli

import (
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

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
