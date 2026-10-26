package config

import (
	"strings"
	"testing"
)

func TestSaveSplitEditKeepsUnknownKeysInPlace(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n    future_field: yes\n    workspace: ~/w\n\n  b:\n    model: haiku\n"
	tests := []struct {
		name string
		src  string
	}{
		{"splice path", src},
		// An alias elsewhere forces the node-encoder fallback.
		{"fallback path", src + "  c: &c\n    model: opus\n  d: *c\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editSharedAndRead(t, tt.src, setModel("a", "sonnet"))
			head, _, _ := strings.Cut(got, "  b:")
			// The unknown key survives between the keys it was written between.
			if !strings.Contains(head, "model: sonnet") || !strings.Contains(head, "model: sonnet\n    future_field: yes\n    workspace: ~/w") {
				t.Errorf("future_field lost or moved:\n%s", got)
			}
		})
	}
}

func TestSaveSplitEditSplicesEntryWithCommentedOutField(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n#    workspace: ~/old\n    max_turns: 5\n\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, setModel("a", "sonnet"))
	if !strings.Contains(got, "#    workspace: ~/old") || !strings.Contains(got, "max_turns: 5") || !strings.Contains(got, "model: sonnet") {
		t.Errorf("edited entry lost content:\n%s", got)
	}
	// A blank line survives only when the splice path handled the file.
	if !strings.HasSuffix(got, "\n\n  b:\n    model: haiku\n") {
		t.Errorf("neighbour not byte-identical (fell back to re-encoding?):\n%s", got)
	}
}

func TestSaveSplitFallbackKeepsEditedEntryComments(t *testing.T) {
	const src = "templates:\n  base: &base\n    model: opus\n  a:\n    model: haiku   # pick one\n    workspace: ~/w # where\n  c: *base\n"
	got := editSharedAndRead(t, src, setModel("a", "sonnet"))
	for _, want := range []string{"model: sonnet # pick one", "workspace: ~/w # where"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestSaveSplitEditNestedMapKeepsOrderAndComments(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n    env:\n      # token\n      Z_TOKEN: \"1\"\n      A_URL: x\n\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, func(cfg *Config) {
		tmpl := cfg.Templates["a"]
		tmpl.Env = map[string]string{"Z_TOKEN": "2", "A_URL": "x"}
		cfg.Templates["a"] = tmpl
	})
	if want := strings.Replace(src, `"1"`, `"2"`, 1); got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitRenameSharedEntryKeepsItsPlaceAndComments(t *testing.T) {
	const src = "# team\ntemplates:\n  a:\n    model: opus\n\n  # the reviewer\n  b: # old name\n    model: sonnet   # pick\n    workspace: ~/review\n\n  c:\n    model: haiku\n"
	f := newNodeFixtureWith(t, src)
	cfg := f.load(t)
	if err := RenameTemplate(cfg, "b", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f.save(t, cfg)

	if want := strings.Replace(src, "  b: # old name", "  reviewer: # old name", 1); readFile(t, f.sharedPath) != want {
		t.Errorf("--- want\n%s--- got\n%s", want, readFile(t, f.sharedPath))
	}
	got := f.load(t)
	if got.TemplateSource("reviewer") != SourceShared || got.Templates["reviewer"].Model != "sonnet" {
		t.Errorf("reviewer = %+v source %q", got.Templates["reviewer"], got.TemplateSource("reviewer"))
	}
	if _, ok := got.Templates["b"]; ok {
		t.Error("old name still present")
	}
}

func TestSaveSplitRenameAndEditSharedEntry(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n\n  b:\n    model: sonnet   # pick\n    workspace: ~/review\n\n  c:\n    model: haiku\n"
	f := newNodeFixtureWith(t, src)
	cfg := f.load(t)
	if err := RenameTemplate(cfg, "b", "reviewer"); err != nil {
		t.Fatal(err)
	}
	setModel("reviewer", "opus")(cfg)
	f.save(t, cfg)

	want := "templates:\n  a:\n    model: opus\n\n  reviewer:\n    model: opus # pick\n    workspace: ~/review\n\n  c:\n    model: haiku\n"
	if got := readFile(t, f.sharedPath); got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitFallbackRenamesInPlace(t *testing.T) {
	const src = "templates:\n  base: &base\n    model: opus\n  b:\n    model: haiku # pick\n  c: *base\n"
	f := newNodeFixtureWith(t, src)
	cfg := f.load(t)
	if err := RenameTemplate(cfg, "b", "reviewer"); err != nil {
		t.Fatal(err)
	}
	f.save(t, cfg)

	got := readFile(t, f.sharedPath)
	if !strings.Contains(got, "reviewer:\n    model: haiku # pick") || strings.Contains(got, "  b:") {
		t.Errorf("rename not done in place:\n%s", got)
	}
	if loaded := f.load(t); loaded.TemplateSource("reviewer") != SourceShared || loaded.Templates["c"].Model != "opus" {
		t.Errorf("reload = %+v", loaded.Templates)
	}
}
