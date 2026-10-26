package config

import (
	"os"
	"strings"
	"testing"
)

// handFormattedShared is how a person writes a shared file: header comments,
// comments on entries, 2-space indents, quoting styles, and key orders
// yaml.Marshal would not produce (model before workspace in shared-b).
const handFormattedShared = `# Shared agent templates
# (maintained by the team)
templates:
  shared-a:
    workspace: ~/somewhere # keep this
    model: opus
  # the reviewer
  shared-b:
    model: sonnet
    workspace: ~/review
    add_dirs:
      - ~/one
      - ~/two
  shared-c:
    model: "haiku"
    env:
      FOO: 'bar'
`

func newNodeFixture(t *testing.T) splitFixture {
	t.Helper()
	dir := t.TempDir()
	return splitFixture{
		dir:        dir,
		leoPath:    writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n"),
		sharedPath: writeFile(t, dir, "shared.yaml", handFormattedShared),
	}
}

// splitAt returns the text before the line starting with marker (exclusive)
// and from it on (inclusive).
func splitAt(t *testing.T, text, marker string) (before, from string) {
	t.Helper()
	i := strings.Index(text, "\n"+marker)
	if i < 0 {
		t.Fatalf("marker %q not found in:\n%s", marker, text)
	}
	return text[:i+1], text[i+1:]
}

func TestSaveSplitEditSharedChangesOnlyThatEntry(t *testing.T) {
	f := newNodeFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["shared-b"]
	tmpl.Model = "opus"
	cfg.Templates["shared-b"] = tmpl
	f.save(t, cfg)

	got := readFile(t, f.sharedPath)
	wantHead, _ := splitAt(t, handFormattedShared, "  shared-b:")
	_, wantTail := splitAt(t, handFormattedShared, "  shared-c:")
	gotHead, gotRest := splitAt(t, got, "  shared-b:")
	_, gotTail := splitAt(t, got, "  shared-c:")
	if gotHead != wantHead {
		t.Errorf("text before shared-b changed:\n--- want\n%s--- got\n%s", wantHead, gotHead)
	}
	if gotTail != wantTail {
		t.Errorf("text from shared-c on changed:\n--- want\n%s--- got\n%s", wantTail, gotTail)
	}
	entry, _, _ := strings.Cut(gotRest, "  shared-c:")
	for _, want := range []string{"model: opus", "workspace: ~/review", "- ~/one"} {
		if !strings.Contains(entry, want) {
			t.Errorf("edited entry missing %q:\n%s", want, entry)
		}
	}
	if strings.Contains(entry, "sonnet") {
		t.Errorf("edited entry kept the old model:\n%s", entry)
	}
}

func TestSaveSplitAddSharedAppendsAndKeepsEverythingElse(t *testing.T) {
	f := newNodeFixture(t)
	cfg := f.load(t)
	if err := cfg.AddTemplate("shared-d", TemplateConfig{Model: "sonnet", Workspace: "~/d"}, true); err != nil {
		t.Fatal(err)
	}
	f.save(t, cfg)

	got := readFile(t, f.sharedPath)
	if !strings.HasPrefix(got, handFormattedShared) {
		t.Errorf("existing text changed:\n%s", got)
	}
	added := strings.TrimPrefix(got, handFormattedShared)
	if added != "  shared-d:\n    workspace: ~/d\n    model: sonnet\n" {
		t.Errorf("appended entry = %q", added)
	}
}

func TestSaveSplitDeleteSharedRemovesOnlyThatEntry(t *testing.T) {
	f := newNodeFixture(t)
	cfg := f.load(t)
	delete(cfg.Templates, "shared-b")
	f.save(t, cfg)

	got := readFile(t, f.sharedPath)
	wantHead, _ := splitAt(t, handFormattedShared, "  shared-b:")
	_, wantTail := splitAt(t, handFormattedShared, "  shared-c:")
	if !strings.HasPrefix(got, strings.TrimSuffix(wantHead, "  # the reviewer\n")) {
		t.Errorf("text before the deleted entry changed:\n%s", got)
	}
	if !strings.HasSuffix(got, wantTail) {
		t.Errorf("text after the deleted entry changed:\n%s", got)
	}
	if strings.Contains(got, "shared-b") || strings.Contains(got, "~/review") {
		t.Errorf("deleted entry still present:\n%s", got)
	}
}

func TestSaveSplitPreservesFourSpaceIndentOfSharedFile(t *testing.T) {
	f := newNodeFixture(t)
	four := "templates:\n    a:\n        model: opus\n    b:\n        model: haiku\n"
	if err := os.WriteFile(f.sharedPath, []byte(four), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := f.load(t)
	tmpl := cfg.Templates["b"]
	tmpl.Model = "sonnet"
	cfg.Templates["b"] = tmpl
	f.save(t, cfg)

	want := "templates:\n    a:\n        model: opus\n    b:\n        model: sonnet\n"
	if got := readFile(t, f.sharedPath); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Constructs yaml.v3 cannot round-trip through yaml.Node: it drops blank lines
// between entries and normalizes the spaces before a line comment to one. The
// rest of an untouched region survives, but not byte for byte. This is the
// acceptance test for a byte-splicing writer, should one replace the node
// encoder.
func TestSaveSplitEditSharedKeepsBlankLinesAndCommentSpacing(t *testing.T) {
	t.Skip("yaml.v3 re-encoding drops blank lines between entries and normalizes spacing before line comments")
	const src = "templates:\n  a:\n    model: opus   # wide\n\n  b:\n    model: haiku\n"
	f := newNodeFixture(t)
	if err := os.WriteFile(f.sharedPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := f.load(t)
	tmpl := cfg.Templates["b"]
	tmpl.Model = "sonnet"
	cfg.Templates["b"] = tmpl
	f.save(t, cfg)

	want := "templates:\n  a:\n    model: opus   # wide\n\n  b:\n    model: sonnet\n"
	if got := readFile(t, f.sharedPath); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
