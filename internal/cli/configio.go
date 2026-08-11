package cli

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// saveConfig writes cfg to the resolved config path, refusing to write one
// that leo could not load back.
//
// The validation matters because config entries reference each other: a
// persistent task names a template, and a template's permission allowlists
// name other templates. Removing a referenced template used to write happily
// and then fail validation on the *next* command — every subsequent leo
// invocation, including the ones you would reach for to fix it — leaving
// hand-editing leo.yaml as the only way out. The web UI has always validated
// before saving (validateAndSave); this brings the CLI in line.
func saveConfig(cfg *config.Config) error {
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to write a config leo cannot load: %w", err)
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
