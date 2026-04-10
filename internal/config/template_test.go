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
			"coding": {Model: "invalid"},
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
			"test": {PermissionMode: "invalid"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid permission mode")
	}
	if !strings.Contains(err.Error(), "templates.test.permission_mode") {
		t.Errorf("error should reference permission_mode, got: %v", err)
	}
}

func TestValidateTemplateValidPermissionMode(t *testing.T) {
	for _, mode := range []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"} {
		cfg := &Config{
			Templates: map[string]TemplateConfig{
				"test": {PermissionMode: mode},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("permission mode %q should be valid: %v", mode, err)
		}
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
	if coding.PermissionMode != "bypassPermissions" {
		t.Errorf("coding.PermissionMode = %q", coding.PermissionMode)
	}
	if coding.RemoteControl == nil || !*coding.RemoteControl {
		t.Error("coding.RemoteControl should be true")
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
			"bad1": {Model: "invalid"},
			"bad2": {PermissionMode: "wrong"},
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
