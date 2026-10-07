package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

const restoreLeoBin = `/opt/my "dev" leo/bin/leo`

// TestRestoreAgentsMigratesLegacyLeoMCPCommand replays records persisted
// while the leo MCP server was a bare "leo" from PATH and asserts the
// relaunched argv/env (and claude's leo-mcp.json) run the daemon's binary.
func TestRestoreAgentsMigratesLegacyLeoMCPCommand(t *testing.T) {
	home := t.TempDir()
	mcpFile := filepath.Join(home, "state", "leo-mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpFile, []byte(`{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opencodeContent := `{"mcp":{"leo":{"command":["leo","mcp-server"],"enabled":true,"environment":{"LEO_API_TOKEN":"tok"},"type":"local"}}}`
	recs := []agentstore.Record{
		{Name: "c", Harness: "", ClaudeArgs: []string{"--model", "sonnet", "--mcp-config", mcpFile}},
		{Name: "x", Harness: "codex", ClaudeArgs: []string{"-a", "never", "-c", `mcp_servers.leo.command="leo"`, "-c", `mcp_servers.leo.args=["mcp-server"]`}},
		{Name: "o", Harness: "opencode", ClaudeArgs: []string{"--model", "a/b"}, Env: map[string]string{"OPENCODE_CONFIG_CONTENT": opencodeContent, "KEEP": "1"}},
	}
	for _, rec := range recs {
		rec.Workspace, rec.SpawnedAt, rec.NoResume = t.TempDir(), time.Now(), true
		if err := agentstore.Save(home, rec); err != nil {
			t.Fatal(err)
		}
	}

	spawner := &fakeAgentSpawner{}
	if n := RestoreAgents(home, "", "tok", spawner, leomcp.Server{Bin: restoreLeoBin}); n != 3 {
		t.Fatalf("restored %d, want 3", n)
	}
	byName := map[string][]string{}
	envs := map[string]map[string]string{}
	for _, c := range spawner.calls {
		byName[c.Name], envs[c.Name] = c.ClaudeArgs, c.Env
	}

	if want := []string{"--model", "sonnet", "--mcp-config", mcpFile}; !reflect.DeepEqual(byName["c"], want) {
		t.Errorf("claude argv = %q, want %q", byName["c"], want)
	}
	raw, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}
	var claudeCfg struct {
		MCPServers map[string]struct{ Command string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &claudeCfg); err != nil || claudeCfg.MCPServers["leo"].Command != restoreLeoBin {
		t.Errorf("leo-mcp.json = %s (err %v), want command %q", raw, err, restoreLeoBin)
	}

	if want := []string{"-a", "never", "-c", `mcp_servers.leo.command="/opt/my \"dev\" leo/bin/leo"`, "-c", `mcp_servers.leo.args=["mcp-server"]`}; !reflect.DeepEqual(byName["x"], want) {
		t.Errorf("codex argv = %q, want %q", byName["x"], want)
	}

	var oc struct {
		MCP map[string]struct{ Command []string } `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(envs["o"]["OPENCODE_CONFIG_CONTENT"]), &oc); err != nil {
		t.Fatal(err)
	}
	if got := oc.MCP["leo"].Command; !reflect.DeepEqual(got, []string{restoreLeoBin, "mcp-server"}) {
		t.Errorf("opencode leo command = %q", got)
	}
	if envs["o"]["KEEP"] != "1" {
		t.Errorf("opencode env lost KEEP: %v", envs["o"])
	}
}

// TestRestoreAgentsLaunchesUnmigratedOnMalformedLeoEntry: a leo entry that
// cannot be migrated neither panics nor blocks the restore; the agent is
// relaunched with its stored argv/env.
func TestRestoreAgentsLaunchesUnmigratedOnMalformedLeoEntry(t *testing.T) {
	home := t.TempDir()
	recs := []agentstore.Record{
		// A migratable entry first: a partial migration must not leak out.
		{Name: "c", ClaudeArgs: []string{"--mcp-config", `{"mcpServers":{"leo":{"command":"leo"}}}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"leo":null}}`, "--model", "sonnet"}},
		{Name: "o", Harness: "opencode", ClaudeArgs: []string{"--model", "a/b"}, Env: map[string]string{"OPENCODE_CONFIG_CONTENT": `{"mcp":{"leo":"x"}}`}},
	}
	for _, rec := range recs {
		rec.Workspace, rec.SpawnedAt, rec.NoResume = t.TempDir(), time.Now(), true
		if err := agentstore.Save(home, rec); err != nil {
			t.Fatal(err)
		}
	}
	spawner := &fakeAgentSpawner{}
	if n := RestoreAgents(home, "", "tok", spawner, leomcp.Server{Bin: restoreLeoBin}); n != 2 {
		t.Fatalf("restored %d, want 2", n)
	}
	for _, c := range spawner.calls {
		for _, rec := range recs {
			if rec.Name == c.Name && (!reflect.DeepEqual(c.ClaudeArgs, rec.ClaudeArgs) || !reflect.DeepEqual(c.Env, rec.Env)) {
				t.Errorf("%s relaunched as %q %v, want stored %q %v", c.Name, c.ClaudeArgs, c.Env, rec.ClaudeArgs, rec.Env)
			}
		}
	}
}
