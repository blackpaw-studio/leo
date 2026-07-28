package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateTemplateModel(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"coding": {Model: "not a model"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid template model")
	}
	if !strings.Contains(err.Error(), "templates.coding.model") {
		t.Errorf("error should reference templates.coding.model, got: %v", err)
	}
}

func TestValidateTemplateValidModel(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"coding": {Model: "sonnet"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid template should pass: %v", err)
	}
}

func TestValidateTemplateMaxTurns(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"test": {MaxTurns: -1},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative max_turns")
	}
	if !strings.Contains(err.Error(), "templates.test.max_turns") {
		t.Errorf("error should reference max_turns, got: %v", err)
	}
}

func TestValidateTemplateChannels(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"test": {Channels: []string{"valid:channel", "bad channel!"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid channel")
	}
	if !strings.Contains(err.Error(), "templates.test.channels[1]") {
		t.Errorf("error should reference channels[1], got: %v", err)
	}
}

func TestValidateTemplateEnvKeys(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"test": {Env: map[string]string{"VALID": "ok", "1INVALID": "bad"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid env key")
	}
	if !strings.Contains(err.Error(), "templates.test.env key") {
		t.Errorf("error should reference env key, got: %v", err)
	}
}

func TestValidateTemplatePermissionMode(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"test": {HarnessOptions: map[string]any{"permission_mode": "invalid"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid permission mode")
	}
	if !strings.Contains(err.Error(), "templates.test.harness_options") || !strings.Contains(err.Error(), "permission_mode") {
		t.Errorf("error should reference templates.test.harness_options permission_mode, got: %v", err)
	}
}

func TestValidateTemplateValidPermissionMode(t *testing.T) {
	for _, mode := range []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"} {
		cfg := &Config{
			Templates: map[string]TemplateConfig{
				"test": {HarnessOptions: map[string]any{"permission_mode": mode}},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("permission mode %q should be valid: %v", mode, err)
		}
	}
}

func TestTemplateMaxTurns(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		tmpl      TemplateConfig
		wantTurns int
	}{
		{
			name:      "uses template max_turns when set",
			cfg:       &Config{Defaults: DefaultsConfig{MaxTurns: 15}},
			tmpl:      TemplateConfig{MaxTurns: 20},
			wantTurns: 20,
		},
		{
			name:      "falls back to defaults",
			cfg:       &Config{Defaults: DefaultsConfig{MaxTurns: 15}},
			tmpl:      TemplateConfig{},
			wantTurns: 15,
		},
		{
			name:      "falls back to built-in when defaults unset",
			cfg:       &Config{},
			tmpl:      TemplateConfig{},
			wantTurns: DefaultMaxTurns,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.TemplateMaxTurns(tt.tmpl); got != tt.wantTurns {
				t.Errorf("TemplateMaxTurns() = %d, want %d", got, tt.wantTurns)
			}
		})
	}
}

func TestTemplatePathExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	yaml := `
templates:
  coding:
    workspace: ~/Developer/agents
    mcp_config: ~/mcp.json
    add_dirs:
      - ~/extra
`
	os.WriteFile(cfgPath, []byte(yaml), 0600)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	tmpl := cfg.Templates["coding"]
	if tmpl.Workspace != filepath.Join(home, "Developer/agents") {
		t.Errorf("workspace not expanded: %q", tmpl.Workspace)
	}
	if tmpl.MCPConfig != filepath.Join(home, "mcp.json") {
		t.Errorf("mcp_config not expanded: %q", tmpl.MCPConfig)
	}
	if len(tmpl.AddDirs) != 1 || tmpl.AddDirs[0] != filepath.Join(home, "extra") {
		t.Errorf("add_dirs not expanded: %v", tmpl.AddDirs)
	}
}

func TestLoadTemplatesFromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	yaml := `
templates:
  coding:
    model: sonnet
    max_turns: 200
    harness_options:
      permission_mode: bypassPermissions
      remote_control: true
    channels:
      - "plugin:telegram@claude-plugins-official"
    env:
      MY_VAR: value
  research:
    model: opus
    max_turns: 50
`
	os.WriteFile(cfgPath, []byte(yaml), 0600)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(cfg.Templates))
	}

	coding := cfg.Templates["coding"]
	if coding.Model != "sonnet" {
		t.Errorf("coding.Model = %q, want sonnet", coding.Model)
	}
	if coding.MaxTurns != 200 {
		t.Errorf("coding.MaxTurns = %d, want 200", coding.MaxTurns)
	}
	if got, _ := coding.HarnessOptions["permission_mode"].(string); got != "bypassPermissions" {
		t.Errorf("coding.HarnessOptions[permission_mode] = %v, want bypassPermissions", coding.HarnessOptions["permission_mode"])
	}
	if got, ok := coding.HarnessOptions["remote_control"].(bool); !ok || !got {
		t.Error("coding.HarnessOptions[remote_control] should be true")
	}
	if len(coding.Channels) != 1 {
		t.Errorf("coding.Channels = %v", coding.Channels)
	}
	if coding.Env["MY_VAR"] != "value" {
		t.Error("coding.Env missing MY_VAR")
	}

	research := cfg.Templates["research"]
	if research.Model != "opus" {
		t.Errorf("research.Model = %q, want opus", research.Model)
	}
}

func TestValidateMultipleTemplateErrors(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"bad1": {Model: "not a model"},
			"bad2": {HarnessOptions: map[string]any{"permission_mode": "wrong"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	// Both should be reported
	errStr := err.Error()
	if !strings.Contains(errStr, "bad1") || !strings.Contains(errStr, "bad2") {
		t.Errorf("expected both templates in error, got: %v", err)
	}
}

func TestEmptyTemplatesValid(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"minimal": {},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty template should be valid: %v", err)
	}
}
