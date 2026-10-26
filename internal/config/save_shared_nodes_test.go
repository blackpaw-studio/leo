package config

import (
	"os"
	"strings"
	"testing"
)

// handFormattedShared is how a person writes a shared file: header comments,
// comments on entries, blank lines between entries, 2-space indents, quoting
// styles, and key orders yaml.Marshal would not produce (model before
// workspace in shared-b).
const handFormattedShared = `# Shared agent templates
# (maintained by the team)
templates:
  shared-a:
    workspace: ~/somewhere   # keep this
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

const sharedBBlock = `  # the reviewer
  shared-b:
    model: sonnet
    workspace: ~/review
    add_dirs:
      - ~/one
      - ~/two

`

func newNodeFixtureWith(t *testing.T, shared string) splitFixture {
	t.Helper()
	dir := t.TempDir()
	return splitFixture{
		dir:        dir,
		leoPath:    writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n"),
		sharedPath: writeFile(t, dir, "shared.yaml", shared),
	}
}

// editSharedAndRead loads the fixture, applies edit to the config, saves and
// returns the shared file's new content.
func editSharedAndRead(t *testing.T, shared string, edit func(*Config)) string {
	t.Helper()
	f := newNodeFixtureWith(t, shared)
	cfg := f.load(t)
	edit(cfg)
	f.save(t, cfg)
	return readFile(t, f.sharedPath)
}

func setModel(name, model string) func(*Config) {
	return func(cfg *Config) {
		tmpl := cfg.Templates[name]
		tmpl.Model = model
		cfg.Templates[name] = tmpl
	}
}

func TestSaveSplitEditSharedChangesOnlyThatEntry(t *testing.T) {
	got := editSharedAndRead(t, handFormattedShared, setModel("shared-b", "opus"))
	want := strings.Replace(handFormattedShared, "model: sonnet", "model: opus", 1)
	if got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitAddSharedAppendsWithTheFilesSeparator(t *testing.T) {
	got := editSharedAndRead(t, handFormattedShared, func(cfg *Config) {
		if err := cfg.AddTemplate("shared-d", TemplateConfig{Model: "sonnet", Workspace: "~/d"}, true); err != nil {
			t.Fatal(err)
		}
	})
	want := handFormattedShared + "\n  shared-d:\n    workspace: ~/d\n    model: sonnet\n"
	if got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitAddSharedToFileWithoutBlankSeparators(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, func(cfg *Config) {
		if err := cfg.AddTemplate("c", TemplateConfig{Model: "sonnet"}, true); err != nil {
			t.Fatal(err)
		}
	})
	if want := src + "  c:\n    model: sonnet\n"; got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitDeleteSharedRemovesOnlyThatEntryAndOneSeparator(t *testing.T) {
	got := editSharedAndRead(t, handFormattedShared, func(cfg *Config) { delete(cfg.Templates, "shared-b") })
	want := strings.Replace(handFormattedShared, sharedBBlock, "", 1)
	if got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitDeleteLastSharedEntryRemovesThePrecedingSeparator(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, func(cfg *Config) { delete(cfg.Templates, "b") })
	if want := "templates:\n  a:\n    model: opus\n"; got != want {
		t.Errorf("--- want\n%s--- got\n%s", want, got)
	}
}

func TestSaveSplitPreservesFourSpaceIndentOfSharedFile(t *testing.T) {
	const src = "templates:\n    a:\n        model: opus\n    b:\n        model: haiku\n"
	got := editSharedAndRead(t, src, setModel("b", "sonnet"))
	if want := "templates:\n    a:\n        model: opus\n    b:\n        model: sonnet\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveSplitEditSharedKeepsBlankLinesAndCommentSpacing(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus   # wide\n\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, setModel("b", "sonnet"))
	if want := "templates:\n  a:\n    model: opus   # wide\n\n  b:\n    model: sonnet\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveSplitEditSharedKeepsLeadingDocumentMarker(t *testing.T) {
	const src = "---\n# head\ntemplates:\n  a:\n    model: opus\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, setModel("b", "sonnet"))
	if want := strings.Replace(src, "haiku", "sonnet", 1); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveSplitEditSharedLeavesNonIndentedSequencesAlone(t *testing.T) {
	const src = "templates:\n  a:\n    model: opus\n    add_dirs:\n    - ~/x\n    - ~/y\n\n  b:\n    model: haiku\n"
	got := editSharedAndRead(t, src, setModel("b", "sonnet"))
	if want := strings.Replace(src, "haiku", "sonnet", 1); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveSplitEditedEntryKeepsItsKeyOrder(t *testing.T) {
	const src = "templates:\n  b:\n    model: sonnet   # pick\n    workspace: ~/review\n    add_dirs:\n      - ~/one\n"
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			"change a later key",
			func(cfg *Config) { t := cfg.Templates["b"]; t.Workspace = "~/other"; cfg.Templates["b"] = t },
			"templates:\n  b:\n    model: sonnet # pick\n    workspace: ~/other\n    add_dirs:\n      - ~/one\n",
		},
		{
			"new key goes last",
			func(cfg *Config) { t := cfg.Templates["b"]; t.MaxTurns = 5; cfg.Templates["b"] = t },
			"templates:\n  b:\n    model: sonnet # pick\n    workspace: ~/review\n    add_dirs:\n      - ~/one\n    max_turns: 5\n",
		},
		{
			"cleared key is dropped",
			func(cfg *Config) { t := cfg.Templates["b"]; t.Workspace = ""; cfg.Templates["b"] = t },
			"templates:\n  b:\n    model: sonnet # pick\n    add_dirs:\n      - ~/one\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := editSharedAndRead(t, src, tt.edit); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// Structures the byte-splicing writer cannot range-map fall back to the node
// encoder; the file must still hold exactly the intended templates.
func TestSaveSplitFallsBackForUnmappableStructures(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"flow mapping", "templates: {a: {model: opus}, b: {model: haiku}}\n"},
		{"anchors and aliases", "templates:\n  a: &base\n    model: opus\n  b: *base\n"},
		{"crlf line endings", "templates:\r\n  a:\r\n    model: opus\r\n  b:\r\n    model: haiku\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNodeFixtureWith(t, tt.src)
			cfg := f.load(t)
			setModel("b", "sonnet")(cfg)
			if err := cfg.AddTemplate("c", TemplateConfig{Model: "opus"}, true); err != nil {
				t.Fatal(err)
			}
			delete(cfg.Templates, "a")
			f.save(t, cfg)

			got := f.load(t)
			if got.Templates["b"].Model != "sonnet" || got.Templates["c"].Model != "opus" {
				t.Errorf("edit or add lost: %+v", got.Templates)
			}
			if _, ok := got.Templates["a"]; ok {
				t.Errorf("deleted template still present: %+v", got.Templates)
			}
		})
	}
}

func TestSaveSplitEditsEmptySharedFile(t *testing.T) {
	f := newNodeFixtureWith(t, "")
	if err := os.WriteFile(f.sharedPath, []byte("# nothing yet\ntemplates:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := f.load(t)
	if err := cfg.AddTemplate("a", TemplateConfig{Model: "opus"}, true); err != nil {
		t.Fatal(err)
	}
	f.save(t, cfg)
	if got := f.load(t); got.Templates["a"].Model != "opus" || got.TemplateSource("a") != SourceShared {
		t.Errorf("a = %+v source %q", got.Templates["a"], got.TemplateSource("a"))
	}
}
