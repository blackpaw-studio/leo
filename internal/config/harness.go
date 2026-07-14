package config

import (
	// Adapters self-register in init; config validation must be able to
	// resolve them.
	_ "github.com/blackpaw-studio/leo/internal/harness/claude"
	_ "github.com/blackpaw-studio/leo/internal/harness/codex"
	_ "github.com/blackpaw-studio/leo/internal/harness/opencode"
)

// DefaultHarnessName is the harness assumed when config specifies none.
const DefaultHarnessName = "claude"

func harnessOrDefault(scope, def string) string {
	if scope != "" {
		return scope
	}
	if def != "" {
		return def
	}
	return DefaultHarnessName
}

func (c *Config) DefaultsHarness() string { return harnessOrDefault(c.Defaults.Harness, "") }

func (c *Config) TaskHarness(t TaskConfig) string {
	return harnessOrDefault(t.Harness, c.Defaults.Harness)
}

func (c *Config) TemplateHarness(t TemplateConfig) string {
	return harnessOrDefault(t.Harness, c.Defaults.Harness)
}

// UsesHarness reports whether any scope in the config — defaults, or any
// template/task — resolves (after the empty-string cascade down from
// defaults) to the named harness.
func (c *Config) UsesHarness(name string) bool {
	if c.DefaultsHarness() == name {
		return true
	}
	for _, t := range c.Templates {
		if c.TemplateHarness(t) == name {
			return true
		}
	}
	for _, t := range c.Tasks {
		if c.TaskHarness(t) == name {
			return true
		}
	}
	return false
}

// mergeHarnessOptions returns a new map with override entries layered over
// base. Neither input is mutated; the result is never nil.
func mergeHarnessOptions(base, override map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

// scopeHarnessOptions layers defaults.harness_options under the scope's own
// options — but only when the scope runs the same harness as defaults;
// options for one harness must not leak into another.
func (c *Config) scopeHarnessOptions(scopeHarness string, opts map[string]any) map[string]any {
	if scopeHarness != c.DefaultsHarness() {
		return mergeHarnessOptions(nil, opts)
	}
	return mergeHarnessOptions(c.Defaults.HarnessOptions, opts)
}

func (c *Config) TaskHarnessOptions(t TaskConfig) map[string]any {
	return c.scopeHarnessOptions(c.TaskHarness(t), t.HarnessOptions)
}

func (c *Config) TemplateHarnessOptions(t TemplateConfig) map[string]any {
	return c.scopeHarnessOptions(c.TemplateHarness(t), t.HarnessOptions)
}
