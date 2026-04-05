package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/templates"
)

func TestFindExistingConfigNone(t *testing.T) {
	dir := t.TempDir()
	cfg, defaultWs := findExistingConfig(dir)

	if cfg != nil {
		t.Error("expected nil config when none exists")
	}
	if defaultWs != filepath.Join(dir, ".leo") {
		t.Errorf("defaultWorkspace = %q, want %q", defaultWs, filepath.Join(dir, ".leo"))
	}
}

func TestFindExistingConfigFound(t *testing.T) {
	dir := t.TempDir()
	leoDir := filepath.Join(dir, ".leo")
	os.MkdirAll(leoDir, 0755)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: leoDir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	config.Save(filepath.Join(leoDir, "leo.yaml"), cfg)

	found, ws := findExistingConfig(dir)
	if found == nil {
		t.Fatal("expected to find existing config")
	}
	if found.Agent.Name != "test" {
		t.Errorf("agent name = %q, want %q", found.Agent.Name, "test")
	}
	if ws != leoDir {
		t.Errorf("workspace = %q, want %q", ws, leoDir)
	}
}

func TestScaffoldWorkspace(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, ".claude", "agents")
	agentPath := filepath.Join(agentDir, "test.md")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	err := scaffoldWorkspace(dir, home, "test", cfg,
		agentDir, agentPath, "# Test Agent",
		filepath.Join(dir, "USER.md"), "TestUser", "developer", "about me", "concise", "UTC")
	if err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	// Verify directories created
	for _, subdir := range []string{"daily", "reports", "state", "config", "scripts"} {
		if _, err := os.Stat(filepath.Join(dir, subdir)); err != nil {
			t.Errorf("directory %s not created", subdir)
		}
	}

	// Verify leo.yaml
	if _, err := os.Stat(filepath.Join(dir, "leo.yaml")); err != nil {
		t.Error("leo.yaml not created")
	}

	// Verify agent file
	data, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal("agent file not created")
	}
	if string(data) != "# Test Agent" {
		t.Errorf("agent content = %q", string(data))
	}

	// Verify USER.md
	data, err = os.ReadFile(filepath.Join(dir, "USER.md"))
	if err != nil {
		t.Fatal("USER.md not created")
	}
	if len(data) == 0 {
		t.Error("USER.md is empty")
	}

	// Verify HEARTBEAT.md
	if _, err := os.Stat(filepath.Join(dir, "HEARTBEAT.md")); err != nil {
		t.Error("HEARTBEAT.md not created")
	}

	// Verify MEMORY.md symlink
	memLink := filepath.Join(dir, "MEMORY.md")
	fi, err := os.Lstat(memLink)
	if err != nil {
		t.Fatal("MEMORY.md symlink not created")
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("MEMORY.md should be a symlink")
	}

	// Verify MCP config
	if _, err := os.Stat(filepath.Join(dir, "config", "mcp-servers.json")); err != nil {
		t.Error("mcp-servers.json not created")
	}
}

func TestScaffoldWorkspaceSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, ".claude", "agents")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	// Pre-create HEARTBEAT.md to verify it's not overwritten
	heartbeatPath := filepath.Join(dir, "HEARTBEAT.md")
	os.WriteFile(heartbeatPath, []byte("custom heartbeat"), 0644)

	// No agent content, no user profile — should skip those
	err := scaffoldWorkspace(dir, home, "test", cfg,
		agentDir, filepath.Join(agentDir, "test.md"), "",
		filepath.Join(dir, "USER.md"), "", "", "", "", "")
	if err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	// HEARTBEAT.md should be unchanged
	data, _ := os.ReadFile(heartbeatPath)
	if string(data) != "custom heartbeat" {
		t.Errorf("HEARTBEAT.md was overwritten: %q", string(data))
	}
}

func TestChooseAgentTemplateReturnsContent(t *testing.T) {
	names := templates.AgentTemplates()
	if len(names) == 0 {
		t.Skip("no agent templates available")
	}
	// The function reads from a bufio.Reader, so we can't easily test the interactive part.
	// But we can verify that templates.RenderAgent works with valid input.
	content, err := templates.RenderAgent(names[0], templates.AgentData{
		Name:      "test",
		Workspace: "/tmp",
	})
	if err != nil {
		t.Fatalf("RenderAgent() error: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty template content")
	}
}
