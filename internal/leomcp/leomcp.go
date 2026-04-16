// Package leomcp manages the auto-injected MCP config that wires Leo's
// built-in MCP server into every supervised Claude process (and task run).
//
// The MCP server itself lives in internal/mcp; this package handles writing
// the small JSON config file Claude Code consumes via --mcp-config and
// generating the matching CLI flag.
package leomcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ConfigPath returns the on-disk path for the Leo-managed MCP config file.
// One file shared across all processes/tasks (the entry is identical;
// per-process scoping happens via env vars at spawn).
func ConfigPath(cfg *config.Config) string {
	return filepath.Join(cfg.StatePath(), "leo-mcp.json")
}

// EnsureConfig writes the Leo-managed MCP config file if it isn't already
// up to date. Idempotent. Returns the absolute path on success.
func EnsureConfig(cfg *config.Config) (string, error) {
	path := ConfigPath(cfg)
	want := buildConfig()

	if existing, err := os.ReadFile(path); err == nil && bytesEqual(existing, want) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return "", fmt.Errorf("write leo MCP config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename leo MCP config: %w", err)
	}
	return path, nil
}

// AppendArg appends `--mcp-config <path>` to args when the daemon's TCP
// listener is enabled. Without the listener the MCP server has nowhere to
// send its HTTP calls, so registering it would only produce confusing
// errors in the supervised Claude's /mcp output.
func AppendArg(args []string, cfg *config.Config) []string {
	if cfg == nil || !cfg.Web.Enabled {
		return args
	}
	path, err := EnsureConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leo: warning: could not write leo MCP config: %v\n", err)
		return args
	}
	return append(args, "--mcp-config", path)
}

func buildConfig() []byte {
	v := map[string]any{
		"mcpServers": map[string]any{
			"leo": map[string]any{
				"command": "leo",
				"args":    []string{"mcp-server"},
			},
		},
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Fall back to a guaranteed-valid hand-rolled string.
		return []byte(`{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}` + "\n")
	}
	return append(out, '\n')
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
