package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// testLeoBin has a space and a quote so the launch config must escape it.
const testLeoBin = `/opt/my "dev" leo/bin/leo`

// TestBuildTemplateArgsLaunchesInjectedLeoMCPBin asserts each harness's
// actual launch config (claude's --mcp-config file, codex's -c argv, and
// opencode's OPENCODE_CONFIG_CONTENT) runs the injected leo binary rather
// than a bare "leo" resolved through the agent's PATH.
func TestBuildTemplateArgsLaunchesInjectedLeoMCPBin(t *testing.T) {
	mcp := leomcp.Server{Bin: testLeoBin}
	wantCommand := []string{testLeoBin, "mcp-server"}

	t.Run("claude", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir()}
		args, _ := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "tok", mcp)
		if got := claudeMCPCommand(t, args); !reflect.DeepEqual(got, wantCommand) {
			t.Errorf("claude leo MCP command = %q, want %q", got, wantCommand)
		}
	})

	t.Run("codex", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir(), Defaults: config.DefaultsConfig{Harness: "codex"}}
		args, _ := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "tok", mcp)
		for _, want := range []string{
			`mcp_servers.leo.command="/opt/my \"dev\" leo/bin/leo"`,
			`mcp_servers.leo.args=["mcp-server"]`,
		} {
			if !containsArg(args, want) {
				t.Errorf("codex argv missing %q; got %q", want, args)
			}
		}
	})

	t.Run("opencode", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir(), Defaults: config.DefaultsConfig{Harness: "opencode"}}
		_, env := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "tok", mcp)
		var parsed struct {
			MCP struct {
				Leo struct {
					Command []string `json:"command"`
				} `json:"leo"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &parsed); err != nil {
			t.Fatalf("OPENCODE_CONFIG_CONTENT: %v (env %v)", err, env)
		}
		if !reflect.DeepEqual(parsed.MCP.Leo.Command, wantCommand) {
			t.Errorf("opencode leo MCP command = %q, want %q", parsed.MCP.Leo.Command, wantCommand)
		}
	})
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// claudeMCPCommand reads the leo-mcp.json that claude argv points
// --mcp-config at and returns the leo server's command line.
func claudeMCPCommand(t *testing.T, args []string) []string {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "--mcp-config" || !strings.HasSuffix(args[i+1], "leo-mcp.json") {
			continue
		}
		raw, err := os.ReadFile(args[i+1])
		if err != nil {
			t.Fatalf("read %s: %v", args[i+1], err)
		}
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
	t.Fatalf("no --mcp-config leo-mcp.json in claude argv %q", args)
	return nil
}

// TestManagerSpawnLaunchesInjectedLeoMCPBin covers the wiring the daemon
// relies on: the Server given to SetLeoMCP reaches the argv the supervisor
// actually launches.
func TestManagerSpawnLaunchesInjectedLeoMCPBin(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetLeoMCP(leomcp.Server{Bin: testLeoBin})

	if _, err := m.SpawnFromTemplate(context.Background(), "a", config.TemplateConfig{Workspace: filepath.Join(home, "ws")}); err != nil {
		t.Fatalf("SpawnFromTemplate: %v", err)
	}
	if sup.spawnCall == nil {
		t.Fatal("supervisor.SpawnAgent not called")
	}
	if got, want := claudeMCPCommand(t, sup.spawnCall.ClaudeArgs), []string{testLeoBin, "mcp-server"}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned leo MCP command = %q, want %q", got, want)
	}
}

// TestResolveRestartArgsFallbackMigratesLegacyLeoMCP covers every fallback
// that replays stored argv/env (ad-hoc agent, deleted template, harness
// changed): the leo MCP command still moves to the injected binary.
func TestResolveRestartArgsFallbackMigratesLegacyLeoMCP(t *testing.T) {
	legacy := agentstore.Record{
		Name: "x", Harness: "codex", Workspace: "/tmp/ws",
		ClaudeArgs: []string{"-a", "never", "-c", `mcp_servers.leo.command="leo"`},
		Env:        map[string]string{"KEEP": "1"},
	}
	cfg := &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{
		"claude-now": {},
	}}
	cases := map[string]agentstore.Record{"ad-hoc": legacy}
	deleted := legacy
	deleted.Template = "gone"
	cases["deleted template"] = deleted
	changed := legacy
	changed.Template = "claude-now"
	cases["harness changed"] = changed

	want := []string{"-a", "never", "-c", `mcp_servers.leo.command="/opt/my \"dev\" leo/bin/leo"`}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			args, env := resolveRestartArgs(cfg, rec, "tok", leomcp.Server{Bin: testLeoBin})
			if !reflect.DeepEqual(args, want) {
				t.Errorf("args = %q, want %q", args, want)
			}
			if env["KEEP"] != "1" {
				t.Errorf("env = %v, want KEEP kept", env)
			}
		})
	}
}
