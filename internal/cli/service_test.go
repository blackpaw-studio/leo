package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestMergeChannelsIntoEnv(t *testing.T) {
	tests := []struct {
		name    string
		proc    config.ProcessConfig
		wantKey string
		wantVal string
		wantLen int
	}{
		{
			name:    "injects LEO_CHANNELS when channels set",
			proc:    config.ProcessConfig{Channels: []string{"plugin:telegram@claude-plugins-official"}},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official",
			wantLen: 1,
		},
		{
			name: "joins multiple channels comma-separated",
			proc: config.ProcessConfig{
				Channels: []string{"plugin:telegram@claude-plugins-official", "plugin:slack@example"},
			},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official,plugin:slack@example",
			wantLen: 1,
		},
		{
			name:    "no channels yields no LEO_CHANNELS entry",
			proc:    config.ProcessConfig{},
			wantLen: 0,
		},
		{
			name: "preserves existing proc.Env entries",
			proc: config.ProcessConfig{
				Env:      map[string]string{"FOO": "bar"},
				Channels: []string{"plugin:telegram@claude-plugins-official"},
			},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official",
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeChannelsIntoEnv(tt.proc)
			if len(got) != tt.wantLen {
				t.Errorf("got %d keys, want %d: %v", len(got), tt.wantLen, got)
			}
			if tt.wantKey != "" {
				if got[tt.wantKey] != tt.wantVal {
					t.Errorf("got[%q] = %q, want %q", tt.wantKey, got[tt.wantKey], tt.wantVal)
				}
			}
		})
	}
}

func TestProcessEnviron(t *testing.T) {
	proc := config.ProcessConfig{
		Channels: []string{"plugin:telegram@claude-plugins-official"},
		Env:      map[string]string{"FOO": "bar"},
	}

	env := processEnviron(proc)

	var sawChannels, sawFoo bool
	for _, line := range env {
		if strings.HasPrefix(line, "LEO_CHANNELS=") {
			sawChannels = true
		}
		if line == "FOO=bar" {
			sawFoo = true
		}
	}
	if !sawChannels {
		t.Error("processEnviron should contain LEO_CHANNELS")
	}
	if !sawFoo {
		t.Error("processEnviron should contain FOO=bar")
	}
}

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
