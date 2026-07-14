package config

import "testing"

func TestRenameTemplate_ReKeysAndRewritesTaskRefs(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"old":   {Model: "sonnet"},
			"other": {Model: "opus"},
		},
		Tasks: map[string]TaskConfig{
			"t1": {Runtime: "persistent", Template: "old"},
			"t2": {Runtime: "persistent", Template: "other"},
		},
	}

	if err := RenameTemplate(cfg, "old", "new"); err != nil {
		t.Fatalf("RenameTemplate: %v", err)
	}

	if _, ok := cfg.Templates["old"]; ok {
		t.Error("old template key still present")
	}
	if got := cfg.Templates["new"].Model; got != "sonnet" {
		t.Errorf("new template Model = %q, want sonnet", got)
	}
	if got := cfg.Tasks["t1"].Template; got != "new" {
		t.Errorf("t1.Template = %q, want new", got)
	}
	if got := cfg.Tasks["t2"].Template; got != "other" {
		t.Errorf("t2.Template = %q, want other (unchanged)", got)
	}
}

func TestRenameTemplate_Errors(t *testing.T) {
	tests := []struct {
		name             string
		oldName, newName string
	}{
		{"empty new name", "old", ""},
		{"old missing", "missing", "new"},
		{"new collides", "old", "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Templates: map[string]TemplateConfig{
				"old":   {Model: "sonnet"},
				"other": {Model: "opus"},
			}}
			if err := RenameTemplate(cfg, tc.oldName, tc.newName); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}
