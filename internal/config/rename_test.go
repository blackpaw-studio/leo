package config

import (
	"reflect"
	"slices"
	"testing"
)

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

func environmentRenameFixture() *Config {
	return &Config{
		Environments: map[string]map[string]string{
			"old":   {"A": "1"},
			"other": {"B": "2"},
		},
		Defaults: DefaultsConfig{Environments: []string{"other", "old"}},
		Templates: map[string]TemplateConfig{
			"t1": {Environments: []string{"old", "other"}},
			"t2": {Environments: []string{"other"}},
		},
		Tasks: map[string]TaskConfig{
			"k1": {Environments: []string{"other", "old"}},
			"k2": {},
		},
	}
}

func TestRenameEnvironment_CascadesPreservingOrder(t *testing.T) {
	next, err := RenameEnvironment(environmentRenameFixture(), "old", "new")
	if err != nil {
		t.Fatalf("RenameEnvironment: %v", err)
	}

	if _, ok := next.Environments["old"]; ok {
		t.Error("old environment key still present")
	}
	if got := next.Environments["new"]["A"]; got != "1" {
		t.Errorf("new environment A = %q, want 1", got)
	}
	if got := next.Environments["other"]["B"]; got != "2" {
		t.Errorf("unrelated environment changed: %v", next.Environments["other"])
	}
	lists := map[string][]string{
		"defaults": next.Defaults.Environments,
		"t1":       next.Templates["t1"].Environments,
		"t2":       next.Templates["t2"].Environments,
		"k1":       next.Tasks["k1"].Environments,
		"k2":       next.Tasks["k2"].Environments,
	}
	want := map[string][]string{
		"defaults": {"other", "new"},
		"t1":       {"new", "other"},
		"t2":       {"other"},
		"k1":       {"other", "new"},
		"k2":       nil,
	}
	for name, got := range lists {
		if !slices.Equal(got, want[name]) {
			t.Errorf("%s environments = %v, want %v", name, got, want[name])
		}
	}
}

func TestRenameEnvironment_DoesNotMutateInput(t *testing.T) {
	cfg := environmentRenameFixture()
	if _, err := RenameEnvironment(cfg, "old", "new"); err != nil {
		t.Fatalf("RenameEnvironment: %v", err)
	}
	if !reflect.DeepEqual(cfg, environmentRenameFixture()) {
		t.Errorf("input config mutated: %+v", cfg)
	}
}

func TestRenameEnvironment_Errors(t *testing.T) {
	tests := []struct {
		name             string
		oldName, newName string
	}{
		{"empty new name", "old", ""},
		{"same name", "old", "old"},
		{"old missing", "missing", "new"},
		{"new collides", "old", "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenameEnvironment(environmentRenameFixture(), tc.oldName, tc.newName); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
