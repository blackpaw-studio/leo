package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestHasMCPServers_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)

	if !config.HasMCPServers(f) {
		t.Error("should return true for valid config with servers")
	}
}

func TestHasMCPServers_EmptyServers(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{}}`), 0644)

	if config.HasMCPServers(f) {
		t.Error("should return false for empty mcpServers")
	}
}

func TestHasMCPServers_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{}`), 0644)

	if config.HasMCPServers(f) {
		t.Error("should return false for empty object")
	}
}

func TestHasMCPServers_MissingFile(t *testing.T) {
	if config.HasMCPServers("/nonexistent/mcp.json") {
		t.Error("should return false for missing file")
	}
}
