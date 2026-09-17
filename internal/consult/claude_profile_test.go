package consult

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

func TestResolveClaudeDispatchProfileDefault(t *testing.T) {
	installedClaudePlugins = func(string) []string { return []string{"b@market", "a@local"} }
	t.Cleanup(func() { installedClaudePlugins = claudeharness.InstalledPluginIDsFromHome })
	got := resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, nil)
	want := claudeharness.Options{MCP: "none", Plugins: "none", EnabledPlugins: []string{"b@market", "a@local"}, StrictMCPConfig: `{"mcpServers":{}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveClaudeDispatchProfileUsesExistingBridge(t *testing.T) {
	got := resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{LeoMCPArgs: []string{"--mcp-config", "/state/leo-mcp.json"}}, nil)
	var gotJSON, wantJSON map[string]any
	if err := json.Unmarshal([]byte(got.StrictMCPConfig), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(leomcp.InlineConfig()), &wantJSON); err != nil {
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
	if got := resolveClaudeDispatchProfile(cfg, tmpl, "dispatch", o, nil); !reflect.DeepEqual(got, o) {
		t.Fatalf("override got %#v", got)
	}
	if got := resolveClaudeDispatchProfile(cfg, tmpl, "agent", o, nil); !reflect.DeepEqual(got, o) {
		t.Fatalf("agent got %#v", got)
	}
}

func TestResolveClaudeDispatchProfileTemplateOverridesPerKey(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{HarnessOptions: map[string]any{"mcp": "none", "plugins": "inherit"}}}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"mcp": "inherit", "plugins": "none"}}
	o := claudeharness.Options{MCP: "inherit", Plugins: "none"}
	got := resolveClaudeDispatchProfile(cfg, tmpl, "dispatch", o, nil)
	if got.MCP != "inherit" || got.Plugins != "none" {
		t.Fatalf("profile = %#v", got)
	}
}

func TestResolveClaudeDispatchProfilePluginHome(t *testing.T) {
	var homes []string
	installedClaudePlugins = func(home string) []string {
		homes = append(homes, home)
		return nil
	}
	t.Cleanup(func() { installedClaudePlugins = claudeharness.InstalledPluginIDsFromHome })

	processHome := t.TempDir()
	t.Setenv("HOME", processHome)
	resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, nil)
	launchHome := t.TempDir()
	resolveClaudeDispatchProfile(&config.Config{}, config.TemplateConfig{}, "dispatch", claudeharness.Options{}, map[string]string{"HOME": launchHome})
	if want := []string{processHome, launchHome}; !reflect.DeepEqual(homes, want) {
		t.Fatalf("plugin homes = %q, want %q", homes, want)
	}
}
