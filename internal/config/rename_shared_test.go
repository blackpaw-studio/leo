package config

import "testing"

func TestRenameSharedTemplateStaysInSharedFile(t *testing.T) {
	tests := []struct {
		name       string
		oldName    string
		newName    string
		wantNewSrc TemplateSource
		wantOldSrc TemplateSource // source of the old name after reload ("" = gone)
		wantOldMdl string
	}{
		{"shared stays shared", "theirs", "ours", SourceShared, "", ""},
		{"override becomes plain local and uncovers shared", "over", "renamed", SourceLocal, SourceShared, "opus"},
		{"local stays local", "mine", "moved", SourceLocal, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSplitFixture(t)
			cfg := f.load(t)
			cfg.Tasks = map[string]TaskConfig{"job": {Template: tt.oldName}}
			wantModel := cfg.Templates[tt.oldName].Model

			if err := RenameTemplate(cfg, tt.oldName, tt.newName); err != nil {
				t.Fatal(err)
			}
			if got := cfg.TemplateSource(tt.newName); got != tt.wantNewSrc {
				t.Fatalf("pre-save source = %q, want %q", got, tt.wantNewSrc)
			}
			f.save(t, cfg)

			got := f.load(t)
			if src := got.TemplateSource(tt.newName); src != tt.wantNewSrc {
				t.Errorf("new source = %q, want %q", src, tt.wantNewSrc)
			}
			if got.Templates[tt.newName].Model != wantModel {
				t.Errorf("new model = %q, want %q", got.Templates[tt.newName].Model, wantModel)
			}
			if src := got.TemplateSource(tt.oldName); src != tt.wantOldSrc {
				t.Errorf("old source = %q, want %q", src, tt.wantOldSrc)
			}
			if tt.wantOldMdl != "" && got.Templates[tt.oldName].Model != tt.wantOldMdl {
				t.Errorf("old model = %q, want %q", got.Templates[tt.oldName].Model, tt.wantOldMdl)
			}
			if got.Tasks["job"].Template != tt.newName {
				t.Errorf("task template = %q, want %q", got.Tasks["job"].Template, tt.newName)
			}
		})
	}
}
