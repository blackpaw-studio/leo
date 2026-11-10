package config

import (
	"strings"
	"testing"
)

const presenceRuleYAML = "appearance_rules:\n  - when: {field: name, op: is, value: x}\n    color: red\n"

// TestAppearanceRulesPresenceRoundTrip: a missing appearance_rules key, an
// empty list and a populated list stay distinct through Load, Save (with a
// rewrite forced by a local edit) and Load, on plain hosts and followers.
func TestAppearanceRulesPresenceRoundTrip(t *testing.T) {
	hosts := []struct {
		name string
		make func(t *testing.T, body string) string
	}{
		{"plain host", func(t *testing.T, body string) string {
			return writeFile(t, t.TempDir(), "leo.yaml", "defaults:\n  model: opus\n"+body)
		}},
		{"follower without cache", func(t *testing.T, body string) string {
			allowProbes(t)
			return writeFile(t, t.TempDir(), "leo.yaml", followerHeader+body)
		}},
		{"follower with cache", func(t *testing.T, body string) string {
			return followerOnDisk(t, saveLeadYAML, body)
		}},
	}
	cases := []struct {
		name    string
		body    string
		present bool
		want    int
	}{
		{"absent", "", false, 0},
		{"empty list", "appearance_rules: []\n", true, 0},
		{"populated", presenceRuleYAML, true, 1},
	}
	for _, h := range hosts {
		for _, c := range cases {
			t.Run(h.name+"/"+c.name, func(t *testing.T) {
				path := h.make(t, c.body)
				cfg := mustLoad(t, path)
				if rules, ok := cfg.LocalAppearanceRules(); ok != c.present || len(rules) != c.want {
					t.Fatalf("loaded: present=%v len=%d, want present=%v len=%d", ok, len(rules), c.present, c.want)
				}
				if cfg.Templates == nil {
					cfg.Templates = map[string]TemplateConfig{}
				}
				cfg.Templates["added"] = TemplateConfig{Model: "sonnet"}
				if err := Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				text := readFile(t, path)
				if got := hasKeyLine(text, "appearance_rules"); got != c.present {
					t.Fatalf("saved file has appearance_rules=%v, want %v:\n%s", got, c.present, text)
				}
				if !strings.Contains(text, "added") {
					t.Fatalf("save did not rewrite the file:\n%s", text)
				}
				rules, ok := mustLoad(t, path).LocalAppearanceRules()
				if ok != c.present || len(rules) != c.want {
					t.Fatalf("reloaded: present=%v len=%d, want present=%v len=%d", ok, len(rules), c.present, c.want)
				}
			})
		}
	}
}
