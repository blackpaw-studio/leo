package testenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptDirs are test directories that need no testenv.Main: this package
// (its own TestMain) and e2e/..., which is behind a build tag and runs
// against its own sandboxed LEO_HOME.
var exemptDirs = map[string]bool{"internal/testenv": true}

// TestEveryTestPackageIsolatesHome is the regression guard for issue #203:
// a package whose tests run without testenv.Main can write into the real
// ~/.claude.json, ~/.claude/ or ~/.codex/config.toml.
func TestEveryTestPackageIsolatesHome(t *testing.T) {
	root := filepath.Join("..", "..")
	testDirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir, _ := filepath.Rel(root, filepath.Dir(path))
		testDirs[filepath.ToSlash(dir)] = append(testDirs[filepath.ToSlash(dir)], path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(testDirs) < 10 {
		t.Fatalf("found only %d test dirs; walk root is wrong", len(testDirs))
	}
	for dir, files := range testDirs {
		if exemptDirs[dir] || dir == "e2e" || strings.HasPrefix(dir, "e2e/") {
			continue
		}
		if !anyFileContains(t, files, "testenv.") {
			t.Errorf("%s: no TestMain calling testenv.Main/Isolate (tests may write to the real home)", dir)
		}
	}
}

func anyFileContains(t *testing.T, files []string, needle string) bool {
	t.Helper()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "func TestMain(") && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}
