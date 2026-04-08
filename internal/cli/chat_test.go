package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncPluginEnv_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	syncPluginEnv("bot123:AAHtoken")

	envFile := filepath.Join(dir, ".claude", "channels", "telegram", ".env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("failed to read .env: %v", err)
	}

	if !strings.Contains(string(data), "TELEGRAM_BOT_TOKEN=bot123:AAHtoken") {
		t.Errorf("env = %q, want bot token", string(data))
	}
}

func TestSyncPluginEnv_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	envDir := filepath.Join(dir, ".claude", "channels", "telegram")
	os.MkdirAll(envDir, 0750)
	os.WriteFile(filepath.Join(envDir, ".env"), []byte("TELEGRAM_BOT_TOKEN=old-token\nOPENAI_API_KEY=sk-abc123\n"), 0600)

	syncPluginEnv("new-bot-token")

	data, err := os.ReadFile(filepath.Join(envDir, ".env"))
	if err != nil {
		t.Fatalf("failed to read .env: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "TELEGRAM_BOT_TOKEN=new-bot-token") {
		t.Errorf("should have new bot token, got: %q", content)
	}
	if !strings.Contains(content, "OPENAI_API_KEY=sk-abc123") {
		t.Errorf("should preserve OPENAI_API_KEY, got: %q", content)
	}
	if strings.Contains(content, "old-token") {
		t.Errorf("should not contain old token, got: %q", content)
	}
}

func TestHasMCPServers_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)

	if !hasMCPServers(f) {
		t.Error("should return true for valid config with servers")
	}
}

func TestHasMCPServers_EmptyServers(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{}}`), 0644)

	if hasMCPServers(f) {
		t.Error("should return false for empty mcpServers")
	}
}

func TestHasMCPServers_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{}`), 0644)

	if hasMCPServers(f) {
		t.Error("should return false for empty object")
	}
}

func TestHasMCPServers_MissingFile(t *testing.T) {
	if hasMCPServers("/nonexistent/mcp.json") {
		t.Error("should return false for missing file")
	}
}
