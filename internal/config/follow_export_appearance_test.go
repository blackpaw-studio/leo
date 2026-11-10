package config

import (
	"encoding/json"
	"testing"
)

func leadExportWith(t *testing.T, extra string) FollowExport {
	t.Helper()
	path := writeFile(t, t.TempDir(), "leo.yaml", "defaults:\n  model: opus\n"+extra)
	exp, err := BuildFollowExport(path, "v1")
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestFollowExportAppearanceRulesKeyAlwaysPresent(t *testing.T) {
	cases := []struct {
		name, yaml string
		wantRules  int
	}{
		{"no rules", "", 0},
		{"empty list", "appearance_rules: []\n", 0},
		{"populated", presenceRuleYAML, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := json.Marshal(leadExportWith(t, c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(data, &top); err != nil {
				t.Fatal(err)
			}
			raw, ok := top["appearance_rules"]
			if !ok {
				t.Fatalf("export lacks appearance_rules: %s", data)
			}
			var rules []map[string]any
			if err := json.Unmarshal(raw, &rules); err != nil || rules == nil {
				t.Fatalf("appearance_rules = %s, want a JSON list (err %v)", raw, err)
			}
			if len(rules) != c.wantRules {
				t.Fatalf("appearance_rules has %d rules, want %d", len(rules), c.wantRules)
			}
		})
	}
}

func TestFollowExportAppearanceRulesKeepsSchema(t *testing.T) {
	if exp := leadExportWith(t, presenceRuleYAML); exp.Schema != 1 {
		t.Fatalf("schema = %d, want 1 (the key is additive)", exp.Schema)
	}
}

func TestFollowDigestCoversAppearanceRules(t *testing.T) {
	none, empty := leadExportWith(t, "").Digest(), leadExportWith(t, "appearance_rules: []\n").Digest()
	if none != empty {
		t.Errorf("digest differs between absent and []: %s vs %s", none, empty)
	}
	withRule := leadExportWith(t, presenceRuleYAML).Digest()
	if withRule == none {
		t.Error("digest ignores appearance_rules")
	}
	changed := leadExportWith(t, "appearance_rules:\n  - when: {field: name, op: is, value: x}\n    color: blue\n").Digest()
	if changed == withRule {
		t.Error("digest ignores a changed rule")
	}
}
