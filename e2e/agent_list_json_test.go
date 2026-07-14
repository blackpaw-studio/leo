//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// TestAgentListJSON verifies `leo agent list --json` emits a well-formed JSON
// array against a live daemon. With no agents running it must still print an
// empty JSON array (not "No agents running.") and exit 0. This locks down the
// JSON contract a later SSH-backed attach picker depends on (leo agent list
// --json read from a remote host).
func TestAgentListJSON(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-agent-list-json-*")
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalCLIConfig), 0o644); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}

	// startDaemon puts the socket at dir/state/leo.sock, matching the
	// HomePath (config file's directory) that `leo agent list` will resolve
	// via loadConfig() -> config.Load(cfgPath).
	startDaemon(t, dir, cfgPath)

	stdout, stderr, code := runLeo(t, dir, nil, "agent", "list", "--json", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("agent list --json exited %d, stderr: %s", code, stderr)
	}

	var records []agent.Record
	if err := json.Unmarshal([]byte(stdout), &records); err != nil {
		t.Fatalf("output is not a JSON array: %v\nstdout: %s", err, stdout)
	}
	if len(records) != 0 {
		t.Fatalf("expected no agents in a fresh daemon, got %d: %+v", len(records), records)
	}
}

const minimalCLIConfig = `defaults:
  model: sonnet
  max_turns: 15
`
