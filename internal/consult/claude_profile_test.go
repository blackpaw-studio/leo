package consult

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

func TestResolveClaudeDispatchProfileDefault(t *testing.T) {
	installedClaudePlugins = func(map[string]string) []string { return []string{"b@market", "a@local"} }
	t.Cleanup(func() { installedClaudePlugins = claudeharness.InstalledPluginIDs })
	got := resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, nil, leomcp.Server{})
	want := claudeharness.Options{MCP: "none", Plugins: "none", EnabledPlugins: []string{"b@market", "a@local"}, StrictMCPConfig: `{"mcpServers":{}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveClaudeDispatchProfileUsesExistingBridge(t *testing.T) {
	got := resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{LeoMCPArgs: []string{"--mcp-config", "/state/leo-mcp.json"}}, nil, leomcp.Server{})
	var gotJSON, wantJSON map[string]any
	if err := json.Unmarshal([]byte(got.StrictMCPConfig), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(leomcp.Server{}.InlineConfig()), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("strict config = %#v, want %#v", gotJSON, wantJSON)
	}
}

func TestResolveClaudeDispatchProfileOverrideAndNonDispatch(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{HarnessOptions: map[string]any{"mcp": "inherit"}}}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"plugins": "inherit"}}
	o := claudeharness.Options{MCP: "inherit", Plugins: "inherit"}
	if got := resolveClaudeDispatchProfile(cfg, tmpl, "dispatch", o, nil, leomcp.Server{}); !reflect.DeepEqual(got, o) {
		t.Fatalf("override got %#v", got)
	}
	if got := resolveClaudeDispatchProfile(cfg, tmpl, "agent", o, nil, leomcp.Server{}); !reflect.DeepEqual(got, o) {
		t.Fatalf("agent got %#v", got)
	}
}

func TestResolveClaudeDispatchProfileTemplateOverridesPerKey(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{HarnessOptions: map[string]any{"mcp": "none", "plugins": "inherit"}}}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"mcp": "inherit", "plugins": "none"}}
	o := claudeharness.Options{MCP: "inherit", Plugins: "none"}
	got := resolveClaudeDispatchProfile(cfg, tmpl, "dispatch", o, nil, leomcp.Server{})
	if got.MCP != "inherit" || got.Plugins != "none" {
		t.Fatalf("profile = %#v", got)
	}
}

func TestResolveClaudeDispatchProfilePluginsReadFromLaunchEnv(t *testing.T) {
	var seen []map[string]string
	installedClaudePlugins = func(env map[string]string) []string {
		seen = append(seen, env)
		return nil
	}
	t.Cleanup(func() { installedClaudePlugins = claudeharness.InstalledPluginIDs })

	launchEnv := map[string]string{"HOME": t.TempDir(), "CLAUDE_CONFIG_DIR": t.TempDir()}
	resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, launchEnv, leomcp.Server{})
	if len(seen) != 1 || !reflect.DeepEqual(seen[0], launchEnv) {
		t.Fatalf("plugins read with env %v, want the launch env %v", seen, launchEnv)
	}
}

// TestResolveClaudeDispatchProfileStrictConfigLaunchesInjectedBin renders
// the strict (MCP none) dispatch through the claude harness and decodes the
// inline JSON straight from argv: the leo server must run the injected
// binary, escaped so a path with spaces and quotes survives.
func TestResolveClaudeDispatchProfileStrictConfigLaunchesInjectedBin(t *testing.T) {
	installedClaudePlugins = func(map[string]string) []string { return nil }
	t.Cleanup(func() { installedClaudePlugins = claudeharness.InstalledPluginIDs })
	const bin = `/opt/my "dev" leo/bin/leo`
	cfg := &config.Config{HomePath: t.TempDir()}
	opts := resolveClaudeDispatchProfile(cfg, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, nil, leomcp.Server{Bin: bin})

	args, err := claudeharness.Claude{}.Args(harness.LaunchSpec{Kind: harness.KindAgent, Name: "d", Workspace: "/tmp/ws", Options: opts, Dispatched: true})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	var inline string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mcp-config" && strings.HasPrefix(args[i+1], "{") {
			inline = args[i+1]
		}
	}
	if inline == "" {
		t.Fatalf("no inline --mcp-config JSON in argv %q", args)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(inline), &parsed); err != nil {
		t.Fatalf("decode %s: %v", inline, err)
	}
	leo := parsed.MCPServers["leo"]
	if leo.Command != bin || !reflect.DeepEqual(leo.Args, []string{"mcp-server"}) {
		t.Errorf("strict leo server = %q %q, want %q [mcp-server]", leo.Command, leo.Args, bin)
	}
}
