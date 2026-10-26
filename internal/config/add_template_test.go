package config

import (
	"strings"
	"testing"
)

func loadSharedFixture(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	leoPath := writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\ntemplates:\n  mine:\n    model: haiku\n")
	writeFile(t, dir, "shared.yaml", "templates:\n  theirs:\n    model: opus\n")
	cfg, err := Load(leoPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAddTemplateShared(t *testing.T) {
	cfg := loadSharedFixture(t)
	if err := cfg.AddTemplate("fresh", TemplateConfig{Model: "sonnet"}, true); err != nil {
		t.Fatal(err)
	}
	if got := cfg.TemplateSource("fresh"); got != SourceShared {
		t.Errorf("source = %q, want shared", got)
	}
	if cfg.Templates["fresh"].Model != "sonnet" {
		t.Errorf("template not stored: %v", cfg.Templates)
	}
}

func TestAddTemplateDefaultsLocal(t *testing.T) {
	cfg := loadSharedFixture(t)
	if err := cfg.AddTemplate("fresh", TemplateConfig{}, false); err != nil {
		t.Fatal(err)
	}
	if got := cfg.TemplateSource("fresh"); got != SourceLocal {
		t.Errorf("source = %q, want local", got)
	}
}

func TestAddTemplateErrors(t *testing.T) {
	tests := []struct {
		name     string
		cfg      func(t *testing.T) *Config
		tmplName string
		shared   bool
		want     string
	}{
		{"shared without templates_file", func(*testing.T) *Config { return &Config{} }, "n", true, "templates_file is not set"},
		{"duplicate local", loadSharedFixture, "mine", false, "already exists"},
		{"duplicate shared", loadSharedFixture, "theirs", false, "already exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg(t).AddTemplate(tt.tmplName, TemplateConfig{}, tt.shared)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAddTemplateSharedWithoutTemplatesFileErrors(t *testing.T) {
	cfg := &Config{}
	if err := cfg.AddTemplate("n", TemplateConfig{}, true); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := cfg.Templates["n"]; ok {
		t.Error("template must not be added on error")
	}
}

func TestAddTemplateIntoNilMap(t *testing.T) {
	cfg := &Config{}
	if err := cfg.AddTemplate("n", TemplateConfig{}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Templates["n"]; !ok {
		t.Error("template missing")
	}
}
