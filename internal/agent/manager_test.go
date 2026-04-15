package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// --- ResolveWorkspace Tests ---

func TestResolveWorkspacePlainName(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := ResolveWorkspace(tmpl, "coding", "myproject", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if workspace != dir {
		t.Errorf("workspace = %q, want %q", workspace, dir)
	}
	if name != "leo-coding-myproject" {
		t.Errorf("name = %q, want leo-coding-myproject", name)
	}
}

func TestResolveWorkspaceWithSlashExistingClone(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo", ".git")
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := ResolveWorkspace(tmpl, "coding", "owner/myrepo", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	expected := filepath.Join(dir, "myrepo")
	if workspace != expected {
		t.Errorf("workspace = %q, want %q", workspace, expected)
	}
	if name != "leo-coding-owner-myrepo" {
		t.Errorf("name = %q, want leo-coding-owner-myrepo", name)
	}
}

func TestResolveWorkspaceNameOverride(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	_, name, err := ResolveWorkspace(tmpl, "coding", "test", "custom-name")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if name != "custom-name" {
		t.Errorf("name = %q, want custom-name", name)
	}
}

func TestResolveWorkspaceDefaultWorkspace(t *testing.T) {
	tmpl := config.TemplateConfig{}

	workspace, _, err := ResolveWorkspace(tmpl, "coding", "test", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if workspace == "" {
		t.Error("expected non-empty default workspace")
	}
}

// --- BuildTemplateArgs Tests ---

func TestBuildTemplateArgsBasic(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
	}
	tmpl := config.TemplateConfig{
		Model:    "opus",
		MaxTurns: 200,
	}

	args := BuildTemplateArgs(cfg, tmpl, "test-agent", "/tmp/workspace")

	assertContainsFlag(t, args, "--model", "opus")
	assertContainsFlag(t, args, "--max-turns", "200")
	assertContainsFlag(t, args, "--add-dir", "/tmp/workspace")
	assertContains(t, args, "--remote-control")
	assertContainsFlag(t, args, "--name", "test-agent")
}

func TestBuildTemplateArgsInheritsDefaults(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:              "haiku",
			MaxTurns:           50,
			PermissionMode:     "auto",
			AllowedTools:       []string{"Read", "Write"},
			AppendSystemPrompt: "be helpful",
		},
	}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")

	assertContainsFlag(t, args, "--model", "haiku")
	assertContainsFlag(t, args, "--max-turns", "50")
	assertContainsFlag(t, args, "--permission-mode", "auto")
	assertContainsFlag(t, args, "--allowed-tools", "Read,Write")
	assertContainsFlag(t, args, "--append-system-prompt", "be helpful")
}

func TestBuildTemplateArgsChannels(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{
		Channels: []string{"plugin:telegram@official", "plugin:slack@custom"},
	}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")

	count := 0
	for _, a := range args {
		if a == "--channels" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 --channels flags, got %d", count)
	}
}

func TestBuildTemplateArgsDevChannels(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{
		Channels:    []string{"plugin:telegram@official"},
		DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
	}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")

	var sawChan, sawDev bool
	for i, a := range args {
		if a == "--channels" && i+1 < len(args) && args[i+1] == "plugin:telegram@official" {
			sawChan = true
		}
		if a == "--dangerously-load-development-channels" && i+1 < len(args) && args[i+1] == "plugin:blackpaw-telegram@blackpaw-plugins" {
			sawDev = true
		}
	}
	if !sawChan {
		t.Errorf("missing --channels flag, args: %v", args)
	}
	if !sawDev {
		t.Errorf("missing --dangerously-load-development-channels flag, args: %v", args)
	}
}

func TestBuildTemplateArgsAgent(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Agent: "my-agent"}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")
	assertContainsFlag(t, args, "--agent", "my-agent")
}

func TestBuildTemplateArgsRemoteControlDisabled(t *testing.T) {
	cfg := &config.Config{}
	rc := false
	tmpl := config.TemplateConfig{RemoteControl: &rc}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")
	for _, a := range args {
		if a == "--remote-control" {
			t.Error("--remote-control should not be present when disabled")
		}
	}
}

// --- Helpers ---

func assertContains(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("expected args to contain %q, got %v", flag, args)
}

func assertContainsFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected args to contain %s %s, got %v", flag, value, args)
}
