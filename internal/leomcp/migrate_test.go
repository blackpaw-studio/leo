package leomcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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

	gotArgs, gotEnv, err := Server{Bin: migrateBin}.MigrateLaunch(&config.Config{}, "codex", args, env)
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

	gotArgs, gotEnv, err := Server{Bin: migrateBin}.MigrateLaunch(&config.Config{}, "opencode", args, env)
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
		gotArgs, _, err := Server{Bin: migrateBin}.MigrateLaunch(&config.Config{HomePath: dir}, name, args, nil)
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
	gotArgs, _, err := Server{}.MigrateLaunch(&config.Config{}, "codex", args, nil)
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

// TestMigrateLaunchLeavesAUserLeoMCPJSONAlone: only the managed file (the
// path EnsureConfig writes) is rewritten, never a same-named user file.
func TestMigrateLaunchLeavesAUserLeoMCPJSONAlone(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	userFile := filepath.Join(project, "leo-mcp.json")
	userJSON := `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`
	if err := os.WriteFile(userFile, []byte(userJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--mcp-config", userFile}
	gotArgs, _, err := Server{Bin: migrateBin}.MigrateLaunch(&config.Config{HomePath: home}, "claude", args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArgs, args) {
		t.Errorf("args = %q, want unchanged", gotArgs)
	}
	if raw, _ := os.ReadFile(userFile); string(raw) != userJSON {
		t.Errorf("user leo-mcp.json rewritten: %s", raw)
	}
}

func TestMigrateLaunchKeepsTheManagedFileMode(t *testing.T) {
	home := t.TempDir()
	path := ConfigPath(&config.Config{HomePath: home})
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A path spelled differently still names the managed file.
	spelled := filepath.Join(home, "state", ".", "leo-mcp.json")
	if _, _, err := (Server{Bin: migrateBin}).MigrateLaunch(&config.Config{HomePath: home}, "claude", []string{"--mcp-config", spelled}, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := leoCommand(t, raw); got[0] != migrateBin {
		t.Fatalf("managed file not migrated: %s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestMigrateLaunchMalformedLeoEntryErrors: a null or non-object value at
// any level is a migration error, never a panic.
func TestMigrateLaunchMalformedLeoEntryErrors(t *testing.T) {
	for _, doc := range []string{
		`{"mcpServers":{"leo":null}}`,
		`{"mcpServers":{"leo":"leo"}}`,
		`{"mcpServers":{"leo":[1]}}`,
		`{"mcpServers":null}`,
		`{"mcpServers":"x"}`,
		`null`,
	} {
		t.Run(doc, func(t *testing.T) {
			// Only a value starting with "{" is taken for inline JSON.
			if strings.HasPrefix(doc, "{") {
				if _, _, err := (Server{Bin: migrateBin}).MigrateLaunch(&config.Config{}, "claude", []string{"--mcp-config", doc}, nil); err == nil {
					t.Error("inline: want a migration error")
				}
			}
			content := strings.Replace(doc, "mcpServers", "mcp", 1)
			if _, _, err := (Server{Bin: migrateBin}).MigrateLaunch(&config.Config{}, "opencode", nil, map[string]string{"OPENCODE_CONFIG_CONTENT": content}); err == nil {
				t.Error("opencode: want a migration error")
			}
		})
	}
}

// TestMigrateLaunchFailureLeavesTheManagedFileUntouched: the file is only
// written once every change validated, so a malformed inline config after
// a valid managed file leaves the file and the launch as they were.
func TestMigrateLaunchFailureLeavesTheManagedFileUntouched(t *testing.T) {
	home := t.TempDir()
	path := ConfigPath(&config.Config{HomePath: home})
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--mcp-config", path, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"leo":null}}`}
	env := map[string]string{"KEEP": "1"}

	gotArgs, gotEnv, err := Server{Bin: migrateBin}.MigrateLaunch(&config.Config{HomePath: home}, "claude", args, env)
	if err == nil {
		t.Fatal("want a migration error")
	}
	if !reflect.DeepEqual(gotArgs, args) || !reflect.DeepEqual(gotEnv, env) {
		t.Errorf("got %q %v, want the original launch", gotArgs, gotEnv)
	}
	if raw, _ := os.ReadFile(path); string(raw) != legacy {
		t.Errorf("managed file migrated despite the failure: %s", raw)
	}
}
