package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretSentinel = "SENTINEL-SECRET-TOKEN-9f3a"

func writeAccountConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const accountConfigBody = `{"numStartups":3,"mcpServers":{
 "chrome":{"type":"stdio","command":"npx","args":["@playwright/mcp"],"headers":{"Authorization":"` + secretSentinel + `"}},
 "other":{"type":"http","url":"https://example.invalid/mcp"}}}`

func assertNoSecret(t *testing.T, warnings []string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, secretSentinel) {
			t.Fatalf("warning leaks a secret: %q", w)
		}
	}
}

func TestResolveMCPServersFindsNamedServers(t *testing.T) {
	dir := t.TempDir()
	writeAccountConfig(t, dir, accountConfigBody)

	defs, warnings := ResolveMCPServers(map[string]string{"CLAUDE_CONFIG_DIR": dir}, []string{"chrome"})

	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(defs) != 1 {
		t.Fatalf("defs = %v, want only chrome", defs)
	}
	var got map[string]any
	if err := json.Unmarshal(defs["chrome"], &got); err != nil || got["command"] != "npx" {
		t.Fatalf("chrome def = %s (%v)", defs["chrome"], err)
	}
}

func TestResolveMCPServersMissingNameWarns(t *testing.T) {
	dir := t.TempDir()
	writeAccountConfig(t, dir, accountConfigBody)

	defs, warnings := ResolveMCPServers(map[string]string{"CLAUDE_CONFIG_DIR": dir}, []string{"chrome", "nope"})

	if len(defs) != 1 || defs["chrome"] == nil {
		t.Fatalf("defs = %v, want chrome only", defs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"nope"`) || !strings.Contains(warnings[0], filepath.Join(dir, ".claude.json")) {
		t.Fatalf("warnings = %v, want one naming nope and the path", warnings)
	}
	assertNoSecret(t, warnings)
}

func TestResolveMCPServersUnusableConfigWarnsAndSkipsEverything(t *testing.T) {
	cases := map[string]string{
		"malformed":         `{"mcpServers": {"chrome": ` + secretSentinel,
		"servers not obj":   `{"mcpServers": ["chrome","` + secretSentinel + `"]}`,
		"entry not obj":     `{"mcpServers": {"chrome": "` + secretSentinel + `"}}`,
		"no mcpServers key": `{"x": 1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeAccountConfig(t, dir, body)

			defs, warnings := ResolveMCPServers(map[string]string{"CLAUDE_CONFIG_DIR": dir}, []string{"chrome"})

			if len(defs) != 0 {
				t.Fatalf("defs = %v, want none", defs)
			}
			if len(warnings) == 0 {
				t.Fatal("want a warning")
			}
			assertNoSecret(t, warnings)
		})
	}
}

func TestResolveMCPServersMissingFileWarns(t *testing.T) {
	dir := t.TempDir()

	defs, warnings := ResolveMCPServers(map[string]string{"CLAUDE_CONFIG_DIR": dir}, []string{"chrome"})

	if len(defs) != 0 || len(warnings) == 0 {
		t.Fatalf("defs=%v warnings=%v, want none and a warning", defs, warnings)
	}
}

func TestResolveMCPServersReadErrorWarnsWithoutErrorText(t *testing.T) {
	readFile := func(string) ([]byte, error) { return nil, errors.New("boom " + secretSentinel) }

	defs, warnings := resolveMCPServers("/x/.claude.json", []string{"chrome"}, readFile)

	if len(defs) != 0 || len(warnings) == 0 {
		t.Fatalf("defs=%v warnings=%v", defs, warnings)
	}
	assertNoSecret(t, warnings)
}

func TestResolveMCPServersLeoIsReserved(t *testing.T) {
	dir := t.TempDir()
	writeAccountConfig(t, dir, `{"mcpServers":{"leo":{"command":"evil"}}}`)

	defs, warnings := ResolveMCPServers(map[string]string{"CLAUDE_CONFIG_DIR": dir}, []string{"leo"})

	if len(defs) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "reserved") {
		t.Fatalf("defs=%v warnings=%v, want reserved warning", defs, warnings)
	}
}

func TestResolveMCPServersFallsBackToHomeWhenConfigDirEmpty(t *testing.T) {
	home := t.TempDir()
	writeAccountConfig(t, home, accountConfigBody)

	defs, warnings := ResolveMCPServers(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": ""}, []string{"chrome"})

	if len(warnings) != 0 || defs["chrome"] == nil {
		t.Fatalf("defs=%v warnings=%v, want chrome from HOME", defs, warnings)
	}
}

const bridgeConfig = `{"mcpServers":{"leo":{"command":"leo","args":["mcp"]}}}`

func chromeDefs() MCPServerDefs {
	return MCPServerDefs{"chrome": json.RawMessage(`{"command":"npx","headers":{"Authorization":"` + secretSentinel + `"}}`)}
}

func serverNames(t *testing.T, config string) []string {
	t.Helper()
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(config), &doc); err != nil {
		t.Fatalf("config %q: %v", config, err)
	}
	names := make([]string, 0, len(doc.MCPServers))
	for n := range doc.MCPServers {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func accountEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	writeAccountConfig(t, dir, accountConfigBody)
	return map[string]string{"CLAUDE_CONFIG_DIR": dir}
}

func TestApplyMCPServersNoneAddsBridgeAndNamedServers(t *testing.T) {
	in := Options{MCP: "none", StrictMCPConfig: bridgeConfig, MCPServers: []string{"chrome"}}

	got, warnings := ApplyMCPServers(in, accountEnv(t))

	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if names := serverNames(t, got.StrictMCPConfig); len(names) != 2 || names[0] != "chrome" || names[1] != "leo" {
		t.Fatalf("servers = %v, want [chrome leo]", names)
	}
	if !got.StrictMCPConfigPrivate {
		t.Fatal("config with added servers must be private")
	}
	if in.StrictMCPConfig != bridgeConfig || in.StrictMCPConfigPrivate {
		t.Fatalf("input mutated: %+v", in)
	}
}

func TestApplyMCPServersNoBridgeYieldsNamedServersOnly(t *testing.T) {
	got, _ := ApplyMCPServers(Options{MCP: "none", StrictMCPConfig: `{"mcpServers":{}}`, MCPServers: []string{"chrome"}}, accountEnv(t))

	if names := serverNames(t, got.StrictMCPConfig); len(names) != 1 || names[0] != "chrome" {
		t.Fatalf("servers = %v, want [chrome]", names)
	}
	if !got.StrictMCPConfigPrivate {
		t.Fatal("want private")
	}
}

func TestApplyMCPServersEveryNameMissingLeavesConfigUntouched(t *testing.T) {
	in := Options{MCP: "none", StrictMCPConfig: bridgeConfig, MCPServers: []string{"nope"}}

	got, warnings := ApplyMCPServers(in, accountEnv(t))

	if got.StrictMCPConfig != bridgeConfig || got.StrictMCPConfigPrivate {
		t.Fatalf("config changed: %+v", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
}

func TestApplyMCPServersBridgeWinsOnNameClash(t *testing.T) {
	dir := t.TempDir()
	writeAccountConfig(t, dir, `{"mcpServers":{"leo":{"command":"evil"},"chrome":{"command":"npx"}}}`)
	in := Options{MCP: "none", StrictMCPConfig: bridgeConfig, MCPServers: []string{"leo", "chrome"}}

	got, warnings := ApplyMCPServers(in, map[string]string{"CLAUDE_CONFIG_DIR": dir})

	if strings.Contains(got.StrictMCPConfig, "evil") {
		t.Fatalf("account server shadowed the bridge: %s", got.StrictMCPConfig)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "reserved") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestApplyMCPServersInheritIgnoresListWithWarning(t *testing.T) {
	in := Options{MCP: "inherit", MCPServers: []string{"chrome"}}

	got, warnings := ApplyMCPServers(in, accountEnv(t))

	if got.StrictMCPConfig != "" || got.StrictMCPConfigPrivate {
		t.Fatalf("inherit changed: %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "inherit") {
		t.Fatalf("warnings = %v, want one inherit warning", warnings)
	}
}

func TestApplyMCPServersUnsetProfileOrNoNamesIsSilent(t *testing.T) {
	for _, in := range []Options{
		{MCPServers: []string{"chrome"}},
		{MCP: "none", StrictMCPConfig: bridgeConfig},
		{MCP: "inherit"},
	} {
		got, warnings := ApplyMCPServers(in, accountEnv(t))
		if len(warnings) != 0 || got.StrictMCPConfig != in.StrictMCPConfig || got.StrictMCPConfigPrivate {
			t.Fatalf("Apply(%+v) = %+v, %v; want unchanged and silent", in, got, warnings)
		}
	}
}

func TestMCPConfigSpillPathRejectsUnusableNames(t *testing.T) {
	if got := MCPConfigSpillPath("/h", "dispatch-1"); got != filepath.Join("/h", "state", "mcp", "dispatch-1.json") {
		t.Fatalf("path = %q", got)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`} {
		if got := MCPConfigSpillPath("/h", name); got != "" {
			t.Fatalf("MCPConfigSpillPath(%q) = %q, want empty", name, got)
		}
	}
	if got := MCPConfigSpillPath("", "x"); got != "" {
		t.Fatalf("empty home gave %q", got)
	}
}

func TestWriteMCPConfigFilePrivateAndOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "mcp", "d.json")

	if err := WriteMCPConfigFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteMCPConfigFile(path, []byte("two")); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v err=%v, want 0600", fi.Mode().Perm(), err)
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
	if err := WriteMCPConfigFile("", []byte("x")); err == nil {
		t.Fatal("empty path must error")
	}
}

func TestMaterializeStrictMCPConfig(t *testing.T) {
	t.Run("not private writes nothing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mcp", "d.json")
		got, err := MaterializeStrictMCPConfig(Options{MCP: "none", StrictMCPConfig: bridgeConfig}, path)
		if err != nil || got.StrictMCPConfigFile != "" {
			t.Fatalf("got %+v err=%v", got, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("file created: %v", err)
		}
	})
	t.Run("private writes the config and sets the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mcp", "d.json")
		in := Options{MCP: "none", StrictMCPConfig: `{"mcpServers":{"chrome":{}}}`, StrictMCPConfigPrivate: true}
		got, err := MaterializeStrictMCPConfig(in, path)
		if err != nil || got.StrictMCPConfigFile != path {
			t.Fatalf("got %+v err=%v", got, err)
		}
		if b, _ := os.ReadFile(path); string(b) != in.StrictMCPConfig {
			t.Fatalf("content = %q", b)
		}
	})
	t.Run("private with an unusable path fails closed", func(t *testing.T) {
		_, err := MaterializeStrictMCPConfig(Options{MCP: "none", StrictMCPConfig: "{}", StrictMCPConfigPrivate: true}, "")
		if err == nil {
			t.Fatal("want error")
		}
	})
}
