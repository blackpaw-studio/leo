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

// stubOwnership fakes the stat'd owner of the existing file, the process
// identity and the chown, so ownership is tested without real root.
func stubOwnership(t *testing.T, fileUID, fileGID, procUID, procGID int, chown func(*os.File, int, int) error) {
	t.Helper()
	prevStat, prevProc, prevChown := statOwner, processOwner, chownTemp
	statOwner = func(os.FileInfo) (int, int, bool) { return fileUID, fileGID, true }
	processOwner = func() (int, int) { return procUID, procGID }
	chownTemp = chown
	t.Cleanup(func() { statOwner, processOwner, chownTemp = prevStat, prevProc, prevChown })
}

func TestSavePreservesOwnerOfExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	writeSeed(t, path, "defaults:\n  model: opus\n", 0600)
	var gotUID, gotGID int
	calls := 0
	stubOwnership(t, 501, 20, 0, 0, func(_ *os.File, uid, gid int) error {
		calls++
		gotUID, gotGID = uid, gid
		return nil
	})

	if err := Save(path, &Config{Defaults: DefaultsConfig{Model: "sonnet"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotUID != 501 || gotGID != 20 {
		t.Errorf("chown calls=%d to %d:%d, want 1 call to 501:20", calls, gotUID, gotGID)
	}
}

func TestSaveSkipsChownWhenOwnerMatchesProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	writeSeed(t, path, "defaults:\n  model: opus\n", 0600)
	stubOwnership(t, 501, 20, 501, 20, func(*os.File, int, int) error {
		t.Error("chown called although owner matches the process")
		return nil
	})

	if err := Save(path, &Config{}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveChownFailureErrorsAndLeavesOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	const original = "defaults:\n  model: opus\n"
	writeSeed(t, path, original, 0600)
	denied := errors.New("operation not permitted")
	stubOwnership(t, 501, 20, 1000, 1000, func(*os.File, int, int) error { return denied })

	err := Save(path, &Config{Defaults: DefaultsConfig{Model: "sonnet"}})
	if !errors.Is(err, denied) {
		t.Fatalf("Save err = %v, want wrapping %v", err, denied)
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Errorf("original modified: %q", got)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("temp files left behind: %v", names)
	}
}

func TestSaveNewFileDoesNotChown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	stubOwnership(t, 501, 20, 1000, 1000, func(*os.File, int, int) error {
		t.Error("chown called for a file that did not exist")
		return nil
	})
	if err := Save(path, &Config{}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveThroughRelativeSymlink(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"dotfiles", "home"} {
		if err := os.Mkdir(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "dotfiles", "leo.yaml")
	link := filepath.Join(root, "home", "leo.yaml")
	writeSeed(t, target, "defaults:\n  model: opus\n", 0644)
	if err := os.Symlink("../dotfiles/leo.yaml", link); err != nil {
		t.Fatal(err)
	}

	if err := Save(link, &Config{Defaults: DefaultsConfig{Model: "sonnet"}}); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v, %v", fi, err)
	}
	loaded, err := Load(target)
	if err != nil || loaded.Defaults.Model != "sonnet" {
		t.Errorf("target = %v, %v; want model sonnet", loaded, err)
	}
	if names := dirNames(t, filepath.Join(root, "home")); len(names) != 1 {
		t.Errorf("home = %v, want only the link", names)
	}
}

func TestSaveSymlinkLoopErrorsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}

	if err := Save(a, &Config{}); err == nil {
		t.Fatal("Save through a symlink loop succeeded, want error")
	}
	if names := dirNames(t, dir); len(names) != 2 {
		t.Errorf("dir = %v, want only the two links", names)
	}
}
