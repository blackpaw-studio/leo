package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSeed(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSaveTightensExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	writeSeed(t, path, "defaults:\n  model: opus\n", 0644)

	if err := Save(path, &Config{Defaults: DefaultsConfig{Model: "sonnet"}}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestSaveFailedWriteLeavesOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	const original = "defaults:\n  model: opus\n"
	writeSeed(t, path, original, 0600)

	diskFull := errors.New("no space left on device")
	prev := writeTemp
	writeTemp = func(f *os.File, data []byte) error {
		_, _ = f.Write(data[:len(data)/2]) // partial write, then the disk fills
		_ = f.Close()
		return diskFull
	}
	t.Cleanup(func() { writeTemp = prev })

	err := Save(path, &Config{Defaults: DefaultsConfig{Model: "sonnet"}})
	if !errors.Is(err, diskFull) {
		t.Fatalf("Save err = %v, want wrapping %v", err, diskFull)
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != original {
		t.Errorf("original modified: %q", got)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("temp files left behind: %v", names)
	}
}

func TestSaveLeavesNoTempFilesOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	for range 2 {
		if err := Save(path, &Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("dir = %v, want only leo.yaml", names)
	}
}

func TestSaveThroughSymlinkKeepsLinkAndUpdatesTarget(t *testing.T) {
	root := t.TempDir()
	dotfiles := filepath.Join(root, "dotfiles")
	home := filepath.Join(root, "home")
	for _, d := range []string{dotfiles, home} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dotfiles, "leo.yaml")
	link := filepath.Join(home, "leo.yaml")
	writeSeed(t, target, "defaults:\n  model: opus\n", 0644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := Save(link, &Config{Defaults: DefaultsConfig{Model: "sonnet"}}); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced by regular file: %v, %v", fi, err)
	}
	loaded, err := Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Model != "sonnet" {
		t.Errorf("target model = %q, want sonnet", loaded.Defaults.Model)
	}
	for _, d := range []string{dotfiles, home} {
		want := 1
		if names := dirNames(t, d); len(names) != want {
			t.Errorf("%s = %v, want %d entry", d, names, want)
		}
	}
}

func TestSaveThroughDanglingSymlinkCreatesTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dotfiles", "leo.yaml")
	if err := os.Mkdir(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "leo.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := Save(link, &Config{Defaults: DefaultsConfig{Model: "sonnet"}}); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v, %v", fi, err)
	}
	if _, err := Load(target); err != nil {
		t.Errorf("target not created: %v", err)
	}
}
