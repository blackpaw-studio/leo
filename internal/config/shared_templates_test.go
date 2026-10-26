package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes content at dir/rel (creating directories) and returns the path.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSharedTemplates(t *testing.T) {
	tests := []struct {
		name      string
		leo       string
		shared    string
		sharedRel string // path of the shared file relative to the leo dir
		wantErr   string
		check     func(t *testing.T, cfg *Config)
	}{
		{
			name:      "merges shared and local with sources",
			sharedRel: "shared.yaml",
			leo:       "templates_file: shared.yaml\ntemplates:\n  mine:\n    model: haiku\n  both:\n    model: haiku\n",
			shared:    "templates:\n  theirs:\n    model: opus\n  both:\n    model: opus\n",
			check: func(t *testing.T, cfg *Config) {
				if len(cfg.Templates) != 3 {
					t.Fatalf("templates = %v, want 3", cfg.Templates)
				}
				for name, want := range map[string]TemplateSource{
					"theirs": SourceShared,
					"mine":   SourceLocal,
					"both":   SourceLocalOverride,
				} {
					if got := cfg.TemplateSource(name); got != want {
						t.Errorf("TemplateSource(%q) = %q, want %q", name, got, want)
					}
				}
				if cfg.Templates["theirs"].Model != "opus" {
					t.Errorf("theirs model = %q", cfg.Templates["theirs"].Model)
				}
			},
		},
		{
			name:      "local template replaces shared whole",
			sharedRel: "shared.yaml",
			leo:       "templates_file: shared.yaml\ntemplates:\n  t:\n    model: haiku\n",
			shared:    "templates:\n  t:\n    model: opus\n    workspace: ~/w\n",
			check: func(t *testing.T, cfg *Config) {
				got := cfg.Templates["t"]
				if got.Model != "haiku" || got.Workspace != "" {
					t.Errorf("t = %+v, want haiku with empty workspace", got)
				}
			},
		},
		{
			name:      "missing file errors naming the path",
			sharedRel: "absent.yaml",
			leo:       "templates_file: absent.yaml\n",
			wantErr:   "absent.yaml",
		},
		{
			name:      "invalid yaml errors naming the path",
			sharedRel: "bad.yaml",
			leo:       "templates_file: bad.yaml\n",
			shared:    "templates: [unclosed\n",
			wantErr:   "bad.yaml",
		},
		{
			name:      "unknown top-level key errors naming the key",
			sharedRel: "shared.yaml",
			leo:       "templates_file: shared.yaml\n",
			shared:    "templates:\n  a:\n    model: opus\nextra: 1\n",
			wantErr:   `"extra"`,
		},
		{
			name:      "relative path resolves against the leo.yaml directory",
			sharedRel: "sub/dir/shared.yaml",
			leo:       "templates_file: sub/dir/shared.yaml\n",
			shared:    "templates:\n  a:\n    model: opus\n",
			check: func(t *testing.T, cfg *Config) {
				if cfg.TemplateSource("a") != SourceShared {
					t.Errorf("a not loaded from shared file: %v", cfg.Templates)
				}
			},
		},
		{
			name:      "tilde path resolves under HOME",
			sharedRel: "",
			leo:       "templates_file: ~/dotfiles/t.yaml\n",
			shared:    "templates:\n  a:\n    model: opus\n",
			check: func(t *testing.T, cfg *Config) {
				if cfg.TemplateSource("a") != SourceShared {
					t.Errorf("a not loaded from shared file: %v", cfg.Templates)
				}
			},
		},
		{
			name:      "shared template paths are expanded",
			sharedRel: "shared.yaml",
			leo:       "templates_file: shared.yaml\n",
			shared:    "templates:\n  a:\n    workspace: ~/w\n    add_dirs: [~/x]\n",
			check: func(t *testing.T, cfg *Config) {
				home, _ := os.UserHomeDir()
				got := cfg.Templates["a"]
				if got.Workspace != filepath.Join(home, "w") || got.AddDirs[0] != filepath.Join(home, "x") {
					t.Errorf("paths not expanded: %+v", got)
				}
			},
		},
		{
			name: "unset templates_file leaves templates untouched",
			leo:  "templates:\n  mine:\n    model: haiku\n",
			check: func(t *testing.T, cfg *Config) {
				if len(cfg.Templates) != 1 || cfg.TemplateSource("mine") != SourceLocal || cfg.HasTemplatesFile() {
					t.Errorf("unexpected state: %v has=%v", cfg.Templates, cfg.HasTemplatesFile())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			leoPath := writeFile(t, dir, "leo.yaml", tt.leo)
			switch {
			case strings.HasPrefix(tt.leo, "templates_file: ~/"):
				home, _ := os.UserHomeDir()
				writeFile(t, home, "dotfiles/t.yaml", tt.shared)
			case tt.sharedRel != "" && tt.shared != "":
				writeFile(t, dir, tt.sharedRel, tt.shared)
			}

			cfg, err := Load(leoPath)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestValidateSharedTemplateMissingEnvironment(t *testing.T) {
	dir := t.TempDir()
	leoPath := writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n")
	writeFile(t, dir, "shared.yaml", "templates:\n  coder:\n    environments: [nope]\n")

	cfg, err := Load(leoPath)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "templates.coder.environments") {
		t.Fatalf("Validate err = %v, want templates.coder.environments", err)
	}
}
