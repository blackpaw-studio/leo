package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSamePath(t *testing.T) {
	dir := t.TempDir()

	t.Run("identical Clean paths", func(t *testing.T) {
		if !SamePath(dir, dir) {
			t.Errorf("SamePath(%q, %q) = false, want true", dir, dir)
		}
	})

	t.Run("Clean-equivalent paths with trailing separator", func(t *testing.T) {
		withSlash := dir + string(os.PathSeparator)
		if !SamePath(dir, withSlash) {
			t.Errorf("SamePath(%q, %q) = false, want true", dir, withSlash)
		}
	})

	t.Run("symlink equivalence", func(t *testing.T) {
		target := filepath.Join(dir, "real")
		if err := os.Mkdir(target, 0o750); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		if !SamePath(link, target) {
			t.Errorf("SamePath(%q, %q) = false, want true (symlink-equivalent)", link, target)
		}
	})

	t.Run("genuinely different dirs", func(t *testing.T) {
		other := t.TempDir()
		if SamePath(dir, other) {
			t.Errorf("SamePath(%q, %q) = true, want false", dir, other)
		}
	})

	t.Run("non-existent path with no Clean match", func(t *testing.T) {
		missing := filepath.Join(dir, "does-not-exist")
		other := filepath.Join(dir, "also-missing")
		if SamePath(missing, other) {
			t.Errorf("SamePath(%q, %q) = true, want false", missing, other)
		}
	})
}
