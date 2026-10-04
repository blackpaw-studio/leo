package bridgemod

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// wantFiles is the mod as Claude Code loads it: manifest, hooks.json, and the
// hooks module with its helper. Tests and generated typings are not shipped.
var wantFiles = []string{
	".claude-plugin/plugin.json",
	"hooks/hooks.json",
	"hooks/protocol.js",
	"hooks/register.js",
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func TestSanitizeVersion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "release tag", in: "v0.31.0", want: "v0.31.0"},
		{name: "git describe dirty", in: "v0.30.1-4-gabc1234-dirty", want: "v0.30.1-4-gabc1234-dirty"},
		{name: "semver build metadata", in: "1.2.3+build.5", want: "1.2.3+build.5"},
		{name: "path separators", in: "feat/x\\y", want: "feat_x_y"},
		{name: "traversal", in: "../../etc", want: "_._.._etc"},
		{name: "dot", in: ".", want: "_"},
		{name: "dotdot", in: "..", want: "_."},
		{name: "spaces and control", in: " dev\tbuild\n", want: "dev_build"},
		{name: "unicode", in: "vé1", want: "v_1"},
		{name: "empty", in: "", wantErr: true},
		{name: "blank", in: "   ", wantErr: true},
		{name: "long is capped", in: strings.Repeat("a", 300), want: strings.Repeat("a", maxVersionLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("sanitizeVersion(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sanitizeVersion(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("sanitizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMaterializeWritesTheMod(t *testing.T) {
	stateDir := t.TempDir()
	dir, err := Materialize(stateDir, "v1.2.3")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	hash, err := contentHash()
	if err != nil {
		t.Fatalf("contentHash: %v", err)
	}
	want := filepath.Join(stateDir, "mods", Name, "v1.2.3-"+hash)
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if got := listFiles(t, dir); strings.Join(got, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("files = %v, want %v", got, wantFiles)
	}
	for _, rel := range wantFiles {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		embedded, err := fs.ReadFile(modFS, root+"/"+rel)
		if err != nil {
			t.Fatalf("embedded %s: %v", rel, err)
		}
		if string(got) != string(embedded) {
			t.Errorf("%s differs from the embedded copy", rel)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifest), `"name": "leo-bridge"`) {
		t.Errorf("manifest does not name the plugin leo-bridge:\n%s", manifest)
	}
	// No temp dirs are left beside the version dir.
	siblings, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if len(siblings) != 1 {
		t.Errorf("parent holds %d entries, want only the version dir", len(siblings))
	}
}

func TestContentHashIsStableAndShort(t *testing.T) {
	a, err := contentHash()
	if err != nil {
		t.Fatal(err)
	}
	b, err := contentHash()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("contentHash changed between calls: %q vs %q", a, b)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("contentHash = %q, want 12 lowercase hex chars", a)
	}
}

func TestHashTreeTracksPathsAndContent(t *testing.T) {
	base := fstest.MapFS{
		"m/a.js":   {Data: []byte("one")},
		"m/b/c.js": {Data: []byte("two")},
	}
	h0, err := hashTree(base, "m")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		fsys fstest.MapFS
		same bool
	}{
		{name: "identical", fsys: fstest.MapFS{"m/a.js": {Data: []byte("one")}, "m/b/c.js": {Data: []byte("two")}}, same: true},
		{name: "content changed", fsys: fstest.MapFS{"m/a.js": {Data: []byte("one!")}, "m/b/c.js": {Data: []byte("two")}}},
		{name: "file renamed", fsys: fstest.MapFS{"m/a2.js": {Data: []byte("one")}, "m/b/c.js": {Data: []byte("two")}}},
		{name: "file added", fsys: fstest.MapFS{"m/a.js": {Data: []byte("one")}, "m/b/c.js": {Data: []byte("two")}, "m/d.js": {Data: nil}}},
		// Boundaries between path and content must not be ambiguous.
		{name: "bytes shifted between files", fsys: fstest.MapFS{"m/a.js": {Data: []byte("onetwo")}, "m/b/c.js": {Data: nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := hashTree(tt.fsys, "m")
			if err != nil {
				t.Fatal(err)
			}
			if (h == h0) != tt.same {
				t.Fatalf("hash equal = %v, want %v", h == h0, tt.same)
			}
		})
	}
}

func TestMaterializeLeavesAnExistingDirUntouched(t *testing.T) {
	stateDir := t.TempDir()
	dir, err := Materialize(stateDir, "v1")
	if err != nil {
		t.Fatalf("first Materialize: %v", err)
	}
	// Claude Code writes generated typings into a loaded mod; a later
	// Materialize must neither remove those nor rewrite our files.
	marker := filepath.Join(dir, "hooks", "register.js")
	if err := os.WriteFile(marker, []byte("// edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(extra, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	again, err := Materialize(stateDir, "v1")
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if again != dir {
		t.Fatalf("second dir = %q, want %q", again, dir)
	}
	if got, _ := os.ReadFile(marker); string(got) != "// edited" {
		t.Errorf("existing hooks/register.js was rewritten (%d bytes)", len(got))
	}
	if _, err := os.Stat(extra); err != nil {
		t.Errorf("extra file was removed: %v", err)
	}
}

func TestMaterializeSeparatesVersions(t *testing.T) {
	stateDir := t.TempDir()
	a, err := Materialize(stateDir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Materialize(stateDir, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("v1 and v2 share %q", a)
	}
}

func TestMaterializeErrors(t *testing.T) {
	fileAsState := filepath.Join(t.TempDir(), "state-file")
	if err := os.WriteFile(fileAsState, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := contentHash()
	if err != nil {
		t.Fatal(err)
	}
	fileAsTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fileAsTarget, "mods", Name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileAsTarget, "mods", Name, "v1-"+hash), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		stateDir string
		version  string
	}{
		{name: "empty state dir", stateDir: "", version: "v1"},
		{name: "relative state dir", stateDir: "state", version: "v1"},
		{name: "empty version", stateDir: t.TempDir(), version: ""},
		{name: "state dir is a file", stateDir: fileAsState, version: "v1"},
		{name: "target is a file", stateDir: fileAsTarget, version: "v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if dir, err := Materialize(tt.stateDir, tt.version); err == nil {
				t.Fatalf("Materialize(%q, %q) = %q, want error", tt.stateDir, tt.version, dir)
			}
		})
	}
}

func TestMaterializeConcurrentCallsAgree(t *testing.T) {
	stateDir := t.TempDir()
	const callers = 16
	var wg sync.WaitGroup
	dirs := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = Materialize(stateDir, "v9")
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if dirs[i] != dirs[0] {
			t.Fatalf("caller %d got %q, caller 0 got %q", i, dirs[i], dirs[0])
		}
	}
	if got := listFiles(t, dirs[0]); strings.Join(got, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("files = %v, want %v", got, wantFiles)
	}
	siblings, err := os.ReadDir(filepath.Dir(dirs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(siblings) != 1 {
		names := make([]string, 0, len(siblings))
		for _, s := range siblings {
			names = append(names, s.Name())
		}
		t.Errorf("parent holds %v, want only the version dir", names)
	}
}
