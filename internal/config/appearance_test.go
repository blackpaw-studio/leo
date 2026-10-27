package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func appearanceRule(field, op, value, color, emoji string) AppearanceRule {
	return AppearanceRule{When: AppearanceCondition{Field: field, Op: op, Value: value}, Color: color, Emoji: emoji}
}

func TestValidateAppearanceRules(t *testing.T) {
	tests := []struct {
		name string
		rule AppearanceRule
		want string // substring of the reason; "" means valid
	}{
		{"color only", appearanceRule("environments", "contains", "prod", "red", ""), ""},
		{"emoji only", appearanceRule("name", "glob", "alfred-*", "", "🎩"), ""},
		{"both", appearanceRule("template", "starts_with", "cod", "teal", "🛠️"), ""},
		{"harness is", appearanceRule("harness", "is", "codex", "gray", ""), ""},
		{"glob with ?", appearanceRule("name", "glob", "worker-?", "blue", ""), ""},
		{"ZWJ family emoji", appearanceRule("name", "is", "a", "", "👨‍👩‍👧‍👦"), ""},
		{"flag emoji", appearanceRule("name", "is", "a", "", "🇺🇸"), ""},
		{"skin-tone emoji", appearanceRule("name", "is", "a", "", "👋🏽"), ""},
		{"keycap emoji", appearanceRule("name", "is", "a", "", "1️⃣"), ""},
		{"unknown field", appearanceRule("machine", "is", "hestia", "red", ""), `field "machine" must be one of name, environments, template, harness`},
		{"empty field", appearanceRule("", "is", "x", "red", ""), `field "" must be one of`},
		{"unknown op", appearanceRule("name", "matches", "x", "red", ""), `op "matches" must be one of is, contains, starts_with, glob`},
		{"empty value", appearanceRule("name", "is", "", "red", ""), "value is required"},
		{"glob class", appearanceRule("name", "glob", "worker-[0-9]", "red", ""), `may only use the * and ? wildcards`},
		{"glob escape", appearanceRule("name", "glob", `worker-\*`, "red", ""), `may only use the * and ? wildcards`},
		{"sets nothing", appearanceRule("name", "is", "x", "", ""), "must set a color, an emoji, or both"},
		{"color off palette", appearanceRule("name", "is", "x", "magenta", ""), `color "magenta" is not in the palette (red, orange, yellow, green, teal, blue, indigo, purple, pink, gray)`},
		{"color wrong case", appearanceRule("name", "is", "x", "Teal", ""), `color "Teal" is not in the palette`},
		{"two emoji", appearanceRule("name", "is", "x", "", "🎩🎩"), "must be a single emoji"},
		{"emoji plus text", appearanceRule("name", "is", "x", "", "🎩 hat"), "must be a single emoji"},
		{"emoji over 64 bytes", appearanceRule("name", "is", "x", "", "a"+strings.Repeat("́", 40)), "must be a single emoji"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rule.Problem()
			if tt.want == "" {
				if got != "" {
					t.Fatalf("Problem() = %q, want valid", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("Problem() = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestValidateReportsAppearanceRulesByIndex(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.AppearanceRules = AppearanceRuleList{
		appearanceRule("name", "is", "ok", "red", ""),
		appearanceRule("name", "is", "x", "magenta", ""),
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an appearance_rules error")
	}
	want := `appearance_rules[1]: color "magenta" is not in the palette`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate() = %v, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), "appearance_rules[0]") {
		t.Fatalf("Validate() flagged the valid rule: %v", err)
	}
}

func TestAppearanceRuleErrorsKeepIndices(t *testing.T) {
	rules := AppearanceRuleList{
		appearanceRule("name", "is", "x", "", ""),
		appearanceRule("name", "is", "ok", "red", ""),
		appearanceRule("nope", "is", "x", "red", ""),
	}
	got := rules.Errors()
	if len(got) != 2 || got[0].Index != 0 || got[1].Index != 2 {
		t.Fatalf("Errors() = %+v, want indices 0 and 2", got)
	}
	if got[0].Reason == "" || got[1].Reason == "" {
		t.Fatalf("Errors() has an empty reason: %+v", got)
	}
}

const lenientAppearanceYAML = `defaults:
    model: sonnet
tasks: {}
appearance_rules:
    - when:
        field: environments
        op: contains
        value: prod
      color: red
    - when: oops
      color:
        nested: map
    - when:
        field: name
        op: glob
        value: alfred-*
      color: pink
      emoji: "🎩"
`

// rulesNode is the parsed appearance_rules value of a leo.yaml document.
func rulesNode(t *testing.T, data string) any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatal(err)
	}
	return doc["appearance_rules"]
}

func TestAppearanceRulesLenientLoad(t *testing.T) {
	path := writeFile(t, t.TempDir(), "leo.yaml", lenientAppearanceYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v, want a malformed rule to load", err)
	}
	if len(cfg.AppearanceRules) != 3 {
		t.Fatalf("loaded %d rules, want 3", len(cfg.AppearanceRules))
	}
	if p := cfg.AppearanceRules[1].Problem(); !strings.Contains(p, "malformed") {
		t.Fatalf("rule 1 Problem() = %q, want malformed", p)
	}
	if p := cfg.AppearanceRules[0].Problem(); p != "" {
		t.Fatalf("rule 0 Problem() = %q, want valid", p)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "appearance_rules[1]: rule is malformed") {
		t.Fatalf("Validate() = %v, want appearance_rules[1] malformed", err)
	}

	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	if !reflect.DeepEqual(rulesNode(t, first), rulesNode(t, lenientAppearanceYAML)) {
		t.Fatalf("Save changed appearance_rules:\n%s", first)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, again); err != nil {
		t.Fatal(err)
	}
	if second := readFile(t, path); second != first {
		t.Fatalf("second round trip is not byte-stable:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

func TestAppearanceRulesNotAListStillLoads(t *testing.T) {
	const doc = "defaults:\n    model: sonnet\ntasks: {}\nappearance_rules:\n    color: red\n"
	path := writeFile(t, t.TempDir(), "leo.yaml", doc)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v, want a non-list appearance_rules to load", err)
	}
	errs := cfg.AppearanceRules.Errors()
	if len(errs) != 1 || errs[0].Index != 0 || !strings.Contains(errs[0].Reason, "must be a list") {
		t.Fatalf("Errors() = %+v, want one list-level error at index 0", errs)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got := rulesNode(t, readFile(t, path)); !reflect.DeepEqual(got, rulesNode(t, doc)) {
		t.Fatalf("Save changed appearance_rules to %#v", got)
	}
}

func TestAppearanceRulesOmittedWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := Save(path, minimalValidConfig()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "appearance_rules") {
		t.Fatalf("an empty rule list was written:\n%s", data)
	}
}

func TestSaveSplitKeepsAppearanceRules(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	rules := AppearanceRuleList{appearanceRule("name", "glob", "alfred-*", "pink", "🎩")}
	cfg.AppearanceRules = rules
	leoTouched, sharedTouched := f.save(t, cfg)
	if !leoTouched || sharedTouched {
		t.Fatalf("touched leo.yaml=%v shared=%v, want only leo.yaml", leoTouched, sharedTouched)
	}
	reloaded := f.load(t)
	if !reflect.DeepEqual(reloaded.AppearanceRules, rules) {
		t.Fatalf("reloaded rules = %#v, want %#v", reloaded.AppearanceRules, rules)
	}
	if strings.Contains(readFile(t, f.sharedPath), "appearance_rules") {
		t.Fatal("appearance_rules leaked into the shared templates file")
	}

	// Editing a shared template afterwards keeps the rules in leo.yaml.
	tmpl := reloaded.Templates["theirs"]
	tmpl.Model = "sonnet"
	reloaded.Templates["theirs"] = tmpl
	f.save(t, reloaded)
	if got := f.load(t).AppearanceRules; !reflect.DeepEqual(got, rules) {
		t.Fatalf("rules after a shared edit = %#v, want %#v", got, rules)
	}
}

func TestValidateAppearanceManual(t *testing.T) {
	if err := ValidateAppearanceColor("teal"); err != nil {
		t.Fatalf("teal: %v", err)
	}
	if err := ValidateAppearanceColor("magenta"); err == nil {
		t.Fatal("magenta: want an error")
	}
	if err := ValidateAppearanceEmoji("🏛️"); err != nil {
		t.Fatalf("🏛️: %v", err)
	}
	for _, bad := range []string{"", "🎩🎩", "ab"} {
		if err := ValidateAppearanceEmoji(bad); err == nil {
			t.Fatalf("%q: want an error", bad)
		}
	}
}
