package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	splitLeoYAML = "templates_file: shared.yaml\ntemplates:\n  mine:\n    model: haiku\n  over:\n    model: haiku\n"
	// hand-formatted: comments and 2-space indents yaml.Marshal would not reproduce
	splitSharedYAML = "# team templates\ntemplates:\n  theirs:\n    model: opus # keep\n    workspace: ~/w\n  over:\n    model: opus\n"
)

type splitFixture struct {
	dir, leoPath, sharedPath string
}

func newSplitFixture(t *testing.T) splitFixture {
	t.Helper()
	dir := t.TempDir()
	return splitFixture{
		dir:        dir,
		leoPath:    writeFile(t, dir, "leo.yaml", splitLeoYAML),
		sharedPath: writeFile(t, dir, "shared.yaml", splitSharedYAML),
	}
}

func (f splitFixture) load(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(f.leoPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// saveExpectingTouched saves cfg and reports which of the two files were
// rewritten (an atomic write installs a new inode, so SameFile tells).
func (f splitFixture) save(t *testing.T, cfg *Config) (leoTouched, sharedTouched bool) {
	t.Helper()
	leoBefore, _ := os.Stat(f.leoPath)
	sharedBefore, _ := os.Stat(f.sharedPath)
	if err := Save(f.leoPath, cfg); err != nil {
		t.Fatal(err)
	}
	leoAfter, _ := os.Stat(f.leoPath)
	sharedAfter, _ := os.Stat(f.sharedPath)
	return !os.SameFile(leoBefore, leoAfter), !os.SameFile(sharedBefore, sharedAfter)
}

func TestSaveSplitEditSharedOnlyTouchesSharedFile(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["theirs"]
	tmpl.Model = "sonnet"
	cfg.Templates["theirs"] = tmpl

	leoTouched, sharedTouched := f.save(t, cfg)
	if leoTouched || !sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want false/true", leoTouched, sharedTouched)
	}
	got := f.load(t)
	if got.Templates["theirs"].Model != "sonnet" || got.TemplateSource("theirs") != SourceShared {
		t.Errorf("theirs = %+v source %q", got.Templates["theirs"], got.TemplateSource("theirs"))
	}
	if got.Templates["over"].Model != "haiku" {
		t.Errorf("override lost: %+v", got.Templates["over"])
	}
	if got.Templates["theirs"].Workspace != cfg.Templates["theirs"].Workspace {
		t.Errorf("workspace changed on round trip")
	}
}

func TestSaveSplitEditedSharedKeepsTildePaths(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["theirs"]
	tmpl.Model = "sonnet"
	cfg.Templates["theirs"] = tmpl
	f.save(t, cfg)

	if body := readFile(t, f.sharedPath); !strings.Contains(body, "workspace: ~/w") {
		t.Errorf("shared file lost its ~ path:\n%s", body)
	}
}

func TestSaveSplitEditLocalLeavesHandFormattedSharedFileUntouched(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["mine"]
	tmpl.Model = "sonnet"
	cfg.Templates["mine"] = tmpl

	leoTouched, sharedTouched := f.save(t, cfg)
	if !leoTouched || sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want true/false", leoTouched, sharedTouched)
	}
	if got := readFile(t, f.sharedPath); got != splitSharedYAML {
		t.Errorf("shared file changed:\n%s", got)
	}
	leo := readFile(t, f.leoPath)
	if !strings.Contains(leo, "templates_file: shared.yaml") || strings.Contains(leo, "theirs") {
		t.Errorf("leo.yaml wrong:\n%s", leo)
	}
}

func TestSaveSplitEditOverrideWritesLeoYaml(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["over"]
	tmpl.Model = "sonnet"
	cfg.Templates["over"] = tmpl

	leoTouched, sharedTouched := f.save(t, cfg)
	if !leoTouched || sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want true/false", leoTouched, sharedTouched)
	}
	got := f.load(t)
	if got.Templates["over"].Model != "sonnet" || got.TemplateSource("over") != SourceLocalOverride {
		t.Errorf("over = %+v source %q", got.Templates["over"], got.TemplateSource("over"))
	}
}

func TestSaveSplitNoChangeTouchesNothing(t *testing.T) {
	f := newSplitFixture(t)
	leoTouched, sharedTouched := f.save(t, f.load(t))
	if leoTouched || sharedTouched {
		t.Errorf("leoTouched=%v sharedTouched=%v, want neither", leoTouched, sharedTouched)
	}
}

func TestSaveSplitDeleteSharedRemovesFromSharedFile(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	delete(cfg.Templates, "theirs")

	leoTouched, sharedTouched := f.save(t, cfg)
	if leoTouched || !sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want false/true", leoTouched, sharedTouched)
	}
	got := f.load(t)
	if _, ok := got.Templates["theirs"]; ok {
		t.Error("theirs still present")
	}
	if got.TemplateSource("over") != SourceLocalOverride {
		t.Errorf("override must survive: %q", got.TemplateSource("over"))
	}
}

func TestSaveSplitDeleteOverrideUncoversShared(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	delete(cfg.Templates, "over")

	leoTouched, sharedTouched := f.save(t, cfg)
	if !leoTouched || sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want true/false", leoTouched, sharedTouched)
	}
	got := f.load(t)
	if got.TemplateSource("over") != SourceShared || got.Templates["over"].Model != "opus" {
		t.Errorf("over = %+v source %q, want shared opus", got.Templates["over"], got.TemplateSource("over"))
	}
}

func TestSaveSplitNewTemplateGoesLocal(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	if err := cfg.AddTemplate("fresh", TemplateConfig{Model: "sonnet"}, false); err != nil {
		t.Fatal(err)
	}
	leoTouched, sharedTouched := f.save(t, cfg)
	if !leoTouched || sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want true/false", leoTouched, sharedTouched)
	}
	if got := f.load(t); got.TemplateSource("fresh") != SourceLocal {
		t.Errorf("source = %q", got.TemplateSource("fresh"))
	}
}

func TestSaveSplitNewSharedTemplateGoesShared(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	if err := cfg.AddTemplate("fresh", TemplateConfig{Model: "sonnet", Workspace: "~/fw"}, true); err != nil {
		t.Fatal(err)
	}
	leoTouched, sharedTouched := f.save(t, cfg)
	if leoTouched || !sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want false/true", leoTouched, sharedTouched)
	}
	got := f.load(t)
	if got.TemplateSource("fresh") != SourceShared || got.Templates["fresh"].Model != "sonnet" {
		t.Errorf("fresh = %+v source %q", got.Templates["fresh"], got.TemplateSource("fresh"))
	}
	if strings.Contains(readFile(t, f.leoPath), "fresh") {
		t.Error("shared template leaked into leo.yaml")
	}
}

func TestSaveSplitKeepsSharedFileModeAndSymlink(t *testing.T) {
	f := newSplitFixture(t)
	real := writeFile(t, f.dir, "repo/real.yaml", splitSharedYAML)
	if err := os.Chmod(real, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.sharedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.sharedPath); err != nil {
		t.Fatal(err)
	}
	cfg := f.load(t)
	tmpl := cfg.Templates["theirs"]
	tmpl.Model = "sonnet"
	cfg.Templates["theirs"] = tmpl
	f.save(t, cfg)

	if fi, err := os.Lstat(f.sharedPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("shared path is no longer a symlink: %v %v", fi, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode().Perm())
	}
	if !strings.Contains(readFile(t, real), "sonnet") {
		t.Error("symlink target not updated")
	}
}

func TestSaveSplitDoesNotLeaveTempFiles(t *testing.T) {
	f := newSplitFixture(t)
	cfg := f.load(t)
	delete(cfg.Templates, "theirs")
	f.save(t, cfg)
	for _, name := range dirNames(t, filepath.Dir(f.sharedPath)) {
		if strings.HasPrefix(name, ".leo") {
			t.Errorf("stray temp file %q", name)
		}
	}
}
