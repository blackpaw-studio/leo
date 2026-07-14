package config

import "testing"

// TestValidateRejectsMovedClaudeFields is table-driven over every
// (scope × field) pair for the claude-harness flat fields that moved to
// harness_options (Task 7). Each case sets exactly one deprecated field on an
// otherwise-minimal valid config and asserts Validate() emits the precise
// migration error. Mirrors TestValidateRejectsRemovedProviders's shape.
func TestValidateRejectsMovedClaudeFields(t *testing.T) {
	validConfig := func() *Config {
		return &Config{
			Defaults: DefaultsConfig{
				Model:    "sonnet",
				MaxTurns: 15,
			},
			Tasks: map[string]TaskConfig{
				"t": {Schedule: "0 * * * *", PromptFile: "HEARTBEAT.md", Enabled: true},
			},
			Templates: map[string]TemplateConfig{
				"x": {},
			},
			HomePath: "/tmp/leo",
		}
	}

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		// --- defaults ---
		{"defaults.permission_mode", func(c *Config) { c.Defaults.DeprecatedPermissionMode = "plan" },
			"defaults.permission_mode has moved to defaults.harness_options.permission_mode (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.bypass_permissions", func(c *Config) { c.Defaults.DeprecatedBypassPermissions = boolPtr(true) },
			"defaults.bypass_permissions has moved to defaults.harness_options.bypass_permissions (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.bypass_permissions explicit false", func(c *Config) { c.Defaults.DeprecatedBypassPermissions = boolPtr(false) },
			"defaults.bypass_permissions has moved to defaults.harness_options.bypass_permissions (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.remote_control", func(c *Config) { c.Defaults.DeprecatedRemoteControl = boolPtr(true) },
			"defaults.remote_control has moved to defaults.harness_options.remote_control (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.remote_control explicit false", func(c *Config) { c.Defaults.DeprecatedRemoteControl = boolPtr(false) },
			"defaults.remote_control has moved to defaults.harness_options.remote_control (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.allowed_tools", func(c *Config) { c.Defaults.DeprecatedAllowedTools = []string{"Read"} },
			"defaults.allowed_tools has moved to defaults.harness_options.allowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.disallowed_tools", func(c *Config) { c.Defaults.DeprecatedDisallowedTools = []string{"Bash"} },
			"defaults.disallowed_tools has moved to defaults.harness_options.disallowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"defaults.append_system_prompt", func(c *Config) { c.Defaults.DeprecatedAppendSystemPrompt = "be nice" },
			"defaults.append_system_prompt has moved to defaults.harness_options.append_system_prompt (claude harness) — see docs/configuration/harnesses.md"},

		// --- templates ---
		{"templates.x.permission_mode", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedPermissionMode = "plan"
			c.Templates["x"] = tmpl
		}, "templates.x.permission_mode has moved to templates.x.harness_options.permission_mode (claude harness) — see docs/configuration/harnesses.md"},
		{"templates.x.remote_control", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedRemoteControl = boolPtr(true)
			c.Templates["x"] = tmpl
		}, "templates.x.remote_control has moved to templates.x.harness_options.remote_control (claude harness) — see docs/configuration/harnesses.md"},
		{"templates.x.agent", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedAgent = "reviewer"
			c.Templates["x"] = tmpl
		}, "templates.x.agent has moved to templates.x.harness_options.agent (claude harness) — see docs/configuration/harnesses.md"},
		{"templates.x.allowed_tools", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedAllowedTools = []string{"Read"}
			c.Templates["x"] = tmpl
		}, "templates.x.allowed_tools has moved to templates.x.harness_options.allowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"templates.x.disallowed_tools", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedDisallowedTools = []string{"Bash"}
			c.Templates["x"] = tmpl
		}, "templates.x.disallowed_tools has moved to templates.x.harness_options.disallowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"templates.x.append_system_prompt", func(c *Config) {
			tmpl := c.Templates["x"]
			tmpl.DeprecatedAppendSystemPrompt = "be nice"
			c.Templates["x"] = tmpl
		}, "templates.x.append_system_prompt has moved to templates.x.harness_options.append_system_prompt (claude harness) — see docs/configuration/harnesses.md"},

		// --- tasks ---
		{"tasks.t.permission_mode", func(c *Config) {
			task := c.Tasks["t"]
			task.DeprecatedPermissionMode = "plan"
			c.Tasks["t"] = task
		}, "tasks.t.permission_mode has moved to tasks.t.harness_options.permission_mode (claude harness) — see docs/configuration/harnesses.md"},
		{"tasks.t.allowed_tools", func(c *Config) {
			task := c.Tasks["t"]
			task.DeprecatedAllowedTools = []string{"Read"}
			c.Tasks["t"] = task
		}, "tasks.t.allowed_tools has moved to tasks.t.harness_options.allowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"tasks.t.disallowed_tools", func(c *Config) {
			task := c.Tasks["t"]
			task.DeprecatedDisallowedTools = []string{"Bash"}
			c.Tasks["t"] = task
		}, "tasks.t.disallowed_tools has moved to tasks.t.harness_options.disallowed_tools (claude harness) — see docs/configuration/harnesses.md"},
		{"tasks.t.append_system_prompt", func(c *Config) {
			task := c.Tasks["t"]
			task.DeprecatedAppendSystemPrompt = "be nice"
			c.Tasks["t"] = task
		}, "tasks.t.append_system_prompt has moved to tasks.t.harness_options.append_system_prompt (claude harness) — see docs/configuration/harnesses.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !contains(got, tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", got, tt.wantErr)
			}
		})
	}
}

// TestValidateAcceptsHarnessOptionsForm confirms the successor form (the same
// values expressed under harness_options) passes Validate() cleanly — the
// migration error only fires for the deprecated flat fields, never for their
// harness_options replacement.
func TestValidateAcceptsHarnessOptionsForm(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
			HarnessOptions: map[string]any{
				"permission_mode":      "plan",
				"bypass_permissions":   true,
				"remote_control":       false,
				"allowed_tools":        []any{"Read"},
				"disallowed_tools":     []any{"Bash"},
				"append_system_prompt": "be nice",
			},
		},
		Tasks: map[string]TaskConfig{
			"t": {Schedule: "0 * * * *", PromptFile: "HEARTBEAT.md", Enabled: true},
		},
		Templates: map[string]TemplateConfig{
			"x": {HarnessOptions: map[string]any{"agent": "reviewer", "remote_control": true}},
		},
		HomePath: "/tmp/leo",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("harness_options form should validate cleanly: %v", err)
	}
}
