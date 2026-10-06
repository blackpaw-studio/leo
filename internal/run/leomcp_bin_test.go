package run

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// TestBuildArgsLaunchesInjectedLeoMCPBin asserts each harness's actual task
// launch config runs the injected leo binary, not a bare "leo" resolved
// through the task process's PATH.
func TestBuildArgsLaunchesInjectedLeoMCPBin(t *testing.T) {
	const bin = `/opt/my "dev" leo/bin/leo`
	mcp := leomcp.Server{Bin: bin}
	wantCommand := []string{bin, "mcp-server"}
	task := config.TaskConfig{Workspace: "/tmp/ws"}

	t.Run("claude", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir()}
		args, _ := buildArgs(cfg, task, "mytask", "do it", "", nil, mcp)
		var path string
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--mcp-config" && strings.HasSuffix(args[i+1], "leo-mcp.json") {
				path = args[i+1]
			}
		}
		if path == "" {
			t.Fatalf("no --mcp-config leo-mcp.json in argv %q", args)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
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
		if got := append([]string{leo.Command}, leo.Args...); !reflect.DeepEqual(got, wantCommand) {
			t.Errorf("claude leo MCP command = %q, want %q", got, wantCommand)
		}
	})

	t.Run("codex", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir(), Defaults: config.DefaultsConfig{Harness: "codex"}}
		args, _ := buildArgs(cfg, task, "mytask", "do it", "", nil, mcp)
		joined := strings.Join(args, "\x00")
		for _, want := range []string{
			"\x00" + `mcp_servers.leo.command="/opt/my \"dev\" leo/bin/leo"` + "\x00",
			"\x00" + `mcp_servers.leo.args=["mcp-server"]` + "\x00",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("codex argv missing %q; got %q", strings.Trim(want, "\x00"), args)
			}
		}
	})

	t.Run("opencode", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir(), Defaults: config.DefaultsConfig{Harness: "opencode"}}
		_, env := buildArgs(cfg, task, "mytask", "do it", "", nil, mcp)
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
