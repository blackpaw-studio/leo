package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardSeatLines(t *testing.T) {
	guard := []string{"cc-plugin-sec-default@builtin"}
	empty := []string{}
	for _, tc := range []struct {
		name    string
		managed managedClaude
		dirs    []guardDir
		want    []string // substrings of the one line per dir
		dont    []string
	}{
		{"prependPlugins lists the guard", managedClaude{PrependPlugins: &guard}, []guardDir{{Label: "~/.claude", OrgType: "claude_pro"}},
			[]string{"~/.claude", "will seat", "prependPlugins"}, []string{"will not seat"}},
		{"an empty prependPlugins keeps a team account unseated", managedClaude{HasSettings: true, PrependPlugins: &empty}, []guardDir{{Label: "env work", OrgType: "claude_team"}},
			[]string{"env work", "will not seat"}, []string{"will seat"}},
		{"unset with managed settings present", managedClaude{HasSettings: true}, []guardDir{{Label: "~/.claude", OrgType: "claude_pro"}},
			[]string{"will seat", "managed settings"}, nil},
		{"unset with a managed MCP file", managedClaude{HasMCP: true}, []guardDir{{Label: "~/.claude"}},
			[]string{"will seat", "managed MCP"}, nil},
		{"unset on a team account", managedClaude{}, []guardDir{{Label: "env work", OrgType: "claude_team"}},
			[]string{"will seat", "claude_team"}, nil},
		{"unset on an enterprise account", managedClaude{}, []guardDir{{Label: "env big", OrgType: "claude_enterprise"}},
			[]string{"will seat", "claude_enterprise"}, nil},
		{"unset on a personal account", managedClaude{}, []guardDir{{Label: "~/.claude", OrgType: "claude_pro"}},
			[]string{"will not seat", "claude_pro"}, []string{"will seat"}},
		{"unset and not logged in", managedClaude{}, []guardDir{{Label: "~/.claude"}},
			[]string{"will not seat", "no account"}, nil},
		{"one line per dir", managedClaude{}, []guardDir{{Label: "a", OrgType: "claude_team"}, {Label: "b", OrgType: "claude_pro"}},
			[]string{"a:", "b:"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := guardSeatLines(tc.managed, tc.dirs)

			if len(lines) != len(tc.dirs) {
				t.Fatalf("lines = %+v, want one per dir", lines)
			}
			joined := ""
			for _, l := range lines {
				if l.Level != "info" {
					t.Fatalf("line %+v is not info: the guard breaks nothing either way", l)
				}
				if !strings.Contains(l.Text, "nothing breaks") {
					t.Fatalf("line %q does not say nothing breaks", l.Text)
				}
				joined += l.Text + "\n"
			}
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("output lacks %q:\n%s", w, joined)
				}
			}
			for _, w := range tc.dont {
				if strings.Contains(joined, w) {
					t.Errorf("output has %q:\n%s", w, joined)
				}
			}
		})
	}
}

func TestProbeManagedClaude(t *testing.T) {
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("nothing managed", func(t *testing.T) {
		got := probeManagedClaude([]string{filepath.Join(t.TempDir(), "absent")})
		if got.HasSettings || got.HasMCP || got.PrependPlugins != nil {
			t.Fatalf("got %+v, want nothing", got)
		}
	})
	t.Run("settings with prependPlugins", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "managed-settings.json"), `{"prependPlugins":["cc-plugin-sec-default@builtin"]}`)
		got := probeManagedClaude([]string{dir})
		if !got.HasSettings || got.PrependPlugins == nil || len(*got.PrependPlugins) != 1 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("an empty list is set, not unset", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "managed-settings.json"), `{"prependPlugins": []}`)
		got := probeManagedClaude([]string{dir})
		if got.PrependPlugins == nil || len(*got.PrependPlugins) != 0 {
			t.Fatalf("got %+v, want a set, empty list", got)
		}
	})
	t.Run("drop-in settings and a managed MCP file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "managed-settings.d", "10-x.json"), `{"model":"opus"}`)
		write(t, filepath.Join(dir, "managed-mcp.json"), `{}`)
		got := probeManagedClaude([]string{dir})
		if !got.HasSettings || !got.HasMCP || got.PrependPlugins != nil {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("unreadable settings still count as present", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "managed-settings.json"), `{not json`)
		got := probeManagedClaude([]string{dir})
		if !got.HasSettings || got.PrependPlugins != nil {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestReadOrgType(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(`{"oauthAccount":{"organizationType":"claude_team","emailAddress":"x@y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readOrgType(path); got != "claude_team" {
		t.Fatalf("org type = %q", got)
	}
	if got := readOrgType(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Fatalf("org type of a missing file = %q", got)
	}
}
