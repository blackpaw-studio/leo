package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	leadRuleYAML = "appearance_rules:\n  - when: {field: name, op: glob, value: lead-*}\n    color: teal\n"
	leadBaseYAML = "defaults:\n  model: opus\ntemplates:\n  lt: {model: opus}\n"
)

// loadFollowerWithExport loads a follower whose cache holds the export of
// leadYAML after edit has rewritten its top-level JSON.
func loadFollowerWithExport(t *testing.T, leadYAML, local string, edit func(map[string]any)) *Config {
	t.Helper()
	allowProbes(t)
	dir := t.TempDir()
	exp := writeLeadCache(t, dir, leadYAML)
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	if data, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := WriteFollowCache(filepath.Join(dir, "state"), FollowCache{
		Host: "lead", SSH: "evan@10.0.2.10", PulledAt: time.Now(),
		LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion, LeadHomeID: exp.HomeID,
		Digest: exp.Digest(), Export: data,
	}); err != nil {
		t.Fatal(err)
	}
	return mustLoad(t, writeFile(t, dir, "leo.yaml", followerHeader+local))
}

func TestFollowAppearanceRulesLayering(t *testing.T) {
	keep := func(map[string]any) {}
	cases := []struct {
		name         string
		lead, local  string
		edit         func(map[string]any)
		wantSource   string
		wantRules    int
		wantWarnings int
	}{
		{"lead list, no local key", leadBaseYAML + leadRuleYAML, "", keep, AppearanceRulesSourceLead, 1, 0},
		{"local list wins whole", leadBaseYAML + leadRuleYAML, presenceRuleYAML + "  - when: {field: name, op: is, value: y}\n    emoji: \"🎩\"\n", keep, AppearanceRulesSourceLocal, 2, 0},
		{"empty local list overrides", leadBaseYAML + leadRuleYAML, "appearance_rules: []\n", keep, AppearanceRulesSourceLocal, 0, 0},
		{"null local key counts as absent", leadBaseYAML + leadRuleYAML, "appearance_rules:\n", keep, AppearanceRulesSourceLead, 1, 0},
		{"export without the key means no lead rules", leadBaseYAML + leadRuleYAML, "", func(d map[string]any) { delete(d, "appearance_rules") }, AppearanceRulesSourceLead, 0, 0},
		{"invalid lead rule is a warning", leadBaseYAML + "appearance_rules:\n  - when: {field: name, op: is, value: x}\n    color: magenta\n", "", keep, AppearanceRulesSourceLead, 1, 1},
		{"lead value not a list", leadBaseYAML, "", func(d map[string]any) { d["appearance_rules"] = "nope" }, AppearanceRulesSourceLead, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := loadFollowerWithExport(t, c.lead, c.local, c.edit)
			if got := cfg.AppearanceRulesSource(); got != c.wantSource {
				t.Errorf("source = %q, want %q", got, c.wantSource)
			}
			if got := len(cfg.EffectiveAppearanceRules()); got != c.wantRules {
				t.Errorf("effective rules = %d, want %d", got, c.wantRules)
			}
			if got := cfg.AppearanceRuleWarnings(); len(got) != c.wantWarnings {
				t.Errorf("warnings = %v, want %d", got, c.wantWarnings)
			}
			if _, ok := cfg.Templates["lt"]; !ok && strings.Contains(c.lead, "lt:") {
				t.Errorf("lead templates were lost: %v", cfg.Templates)
			}
		})
	}
}

func TestFollowAppearanceRulesLeadListIsTheLeads(t *testing.T) {
	cfg := loadFollowerWithExport(t, leadBaseYAML+leadRuleYAML, "", func(map[string]any) {})
	want := AppearanceRule{When: AppearanceCondition{Field: "name", Op: "glob", Value: "lead-*"}, Color: "teal"}
	if got := cfg.EffectiveAppearanceRules(); !reflect.DeepEqual(got, AppearanceRuleList{want}) {
		t.Fatalf("effective = %#v, want %#v", got, want)
	}
	if _, ok := cfg.LocalAppearanceRules(); ok {
		t.Fatal("lead rules leaked into the local key")
	}
}

func TestAppearanceRulesSourceOnPlainHost(t *testing.T) {
	cfg := mustLoad(t, writeFile(t, t.TempDir(), "leo.yaml", "defaults:\n  model: opus\n"))
	if got := cfg.AppearanceRulesSource(); got != AppearanceRulesSourceLocal {
		t.Fatalf("source = %q, want local", got)
	}
}

func TestFollowerSaveNeverWritesLeadRules(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	writeLeadCache(t, dir, leadBaseYAML+leadRuleYAML)
	path := writeFile(t, dir, "leo.yaml", followerHeader)
	cfg := mustLoad(t, path)
	cfg.Templates["added"] = TemplateConfig{Model: "sonnet"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); hasKeyLine(text, "appearance_rules") {
		t.Fatalf("lead rules were written to leo.yaml:\n%s", text)
	}
}
