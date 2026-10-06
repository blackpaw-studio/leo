package leomcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const migrateBin = `/opt/my "dev" leo/bin/leo`

// Legacy launches below are verbatim shapes of records persisted before the
// leo MCP server ran the daemon's own binary: a bare "leo" from PATH.

func TestMigrateLaunchCodexRewritesOnlyTheLeoCommand(t *testing.T) {
	args := []string{
		"-a", "never", "--model", "gpt-5",
		"-c", `mcp_servers.other.command="leo"`,
		"-c", `mcp_servers.leo.command="leo"`,
		"-c", `mcp_servers.leo.args=["mcp-server"]`,
		"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME"]`,
	}
	env := map[string]string{"FOO": "bar"}

	gotArgs, gotEnv, err := Server{Bin: migrateBin}.MigrateLaunch("codex", args, env)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), args...)
	want[7] = `mcp_servers.leo.command="/opt/my \"dev\" leo/bin/leo"`
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args =\n %q\nwant\n %q", gotArgs, want)
	}
	if args[7] != `mcp_servers.leo.command="leo"` {
		t.Error("input args were mutated")
	}
	if !reflect.DeepEqual(gotEnv, env) {
		t.Errorf("env = %v, want unchanged %v", gotEnv, env)
	}
}

func TestMigrateLaunchOpencodeRewritesOnlyTheLeoCommand(t *testing.T) {
	content := `{"mcp":{"leo":{"command":["leo","mcp-server"],"enabled":true,"environment":{"LEO_API_TOKEN":"tok"},"timeout":1920000,"type":"local"},"other":{"command":["leo","x"]}},"permission":{"bash":"ask"}}`
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": content, "FOO": "bar"}
	args := []string{"--model", "x/y"}

	gotArgs, gotEnv, err := Server{Bin: migrateBin}.MigrateLaunch("opencode", args, env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArgs, args) || gotEnv["FOO"] != "bar" {
		t.Errorf("untouched parts changed: args %q env %v", gotArgs, gotEnv)
	}
	if env["OPENCODE_CONFIG_CONTENT"] != content {
		t.Error("input env was mutated")
	}
	var got, want any
	if err := json.Unmarshal([]byte(gotEnv["OPENCODE_CONFIG_CONTENT"]), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(content), &want); err != nil {
		t.Fatal(err)
	}
	want.(map[string]any)["mcp"].(map[string]any)["leo"].(map[string]any)["command"] = []any{migrateBin, "mcp-server"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OPENCODE_CONFIG_CONTENT =\n %v\nwant\n %v", got, want)
	}
}

func TestMigrateLaunchClaudeRewritesConfigFileAndInlineConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "leo-mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	userCfg := filepath.Join(dir, "user-mcp.json")
	userJSON := `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`
	if err := os.WriteFile(userCfg, []byte(userJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	inline := `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`
	args := []string{"--mcp-config", userCfg, "--mcp-config", path, "--strict-mcp-config", "--mcp-config", inline, "--model", "sonnet"}

	for _, name := range []string{"", "claude"} {
		gotArgs, _, err := Server{Bin: migrateBin}.MigrateLaunch(name, args, nil)
		if err != nil {
			t.Fatal(err)
		}
		if gotArgs[1] != userCfg || gotArgs[3] != path || gotArgs[7] != "--model" {
			t.Errorf("harness %q: untouched argv changed: %q", name, gotArgs)
		}
		if got := leoCommand(t, []byte(gotArgs[6])); !reflect.DeepEqual(got, []string{migrateBin, "mcp-server"}) {
			t.Errorf("harness %q: inline leo command = %q", name, got)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := leoCommand(t, raw); !reflect.DeepEqual(got, []string{migrateBin, "mcp-server"}) {
			t.Errorf("harness %q: leo-mcp.json leo command = %q", name, got)
		}
	}
	if raw, _ := os.ReadFile(userCfg); string(raw) != userJSON {
		t.Errorf("a user --mcp-config file was rewritten: %s", raw)
	}
}

func TestMigrateLaunchZeroServerIsNoop(t *testing.T) {
	args := []string{"-c", `mcp_servers.leo.command="leo"`}
	gotArgs, _, err := Server{}.MigrateLaunch("codex", args, nil)
	if err != nil || !reflect.DeepEqual(gotArgs, args) {
		t.Errorf("got %q, %v; want unchanged", gotArgs, err)
	}
}

func leoCommand(t *testing.T, raw []byte) []string {
	t.Helper()
	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	leo := parsed.MCPServers["leo"]
	return append([]string{leo.Command}, leo.Args...)
}
