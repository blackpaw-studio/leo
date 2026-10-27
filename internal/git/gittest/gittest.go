// Package gittest builds real git fixtures in temp dirs for tests that run
// git itself rather than faking it.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

// Run runs git in dir and fails the test on error, returning trimmed output.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write writes content to path, creating parents.
func Write(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// NewRepo makes <tmp>/repo with a.txt, b.txt and a .gitignore ignoring
// ignored.log, committed on main. It sets testenv.Git for the test.
func NewRepo(t testing.TB) string {
	t.Helper()
	testenv.Git(t)
	dir := filepath.Join(t.TempDir(), "repo")
	Run(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	Write(t, filepath.Join(dir, "a.txt"), "a\n")
	Write(t, filepath.Join(dir, "b.txt"), "b\n")
	Write(t, filepath.Join(dir, ".gitignore"), "ignored.log\n")
	Run(t, dir, "add", ".")
	Run(t, dir, "commit", "-qm", "init")
	return dir
}

// Status is `status --porcelain=v2 --branch --untracked-files=all`.
func Status(t testing.TB, dir string) string {
	t.Helper()
	return Run(t, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=all")
}

// CopyWorkingFiles copies src into dst skipping the top-level .git, as the
// git-mode workspace rsync (--exclude=/.git) does.
func CopyWorkingFiles(t testing.TB, src, dst string) {
	t.Helper()
	if err := copyTree(src, dst, true); err != nil {
		t.Fatal(err)
	}
}

// CopyDir copies a tree as rsync -a would.
func CopyDir(src, dst string) error { return copyTree(src, dst, false) }

func copyTree(src, dst string, skipTopGit bool) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if skipTopGit && rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // a linked worktree's .git file
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

var (
	superOnce sync.Once
	superRoot string
	superErr  string
)

// SuperWithSubmodules returns a fresh copy of a superproject with:
//   - libs/a: absorbed, holding its own absorbed submodule "deep"
//   - libs/inl: a named submodule whose .git is a directory (inline)
//   - libs/emb: an embedded repo (a gitlink with no .gitmodules entry)
//   - libs/u: a deinitialised submodule (uninit)
//
// The fixture is built once per test binary and copied per call; every
// pointer inside it is relative, so the copy is self-contained (submodule
// URLs still name the template's bare repos).
func SuperWithSubmodules(t testing.TB) string {
	t.Helper()
	testenv.Git(t)
	superOnce.Do(func() { superRoot, superErr = buildSuper() })
	if superErr != "" {
		t.Fatal(superErr)
	}
	dst := filepath.Join(t.TempDir(), "fixture")
	if err := CopyDir(superRoot, dst); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dst, "super")
}

// buildSuper builds the template under the test binary's private HOME
// (testenv.Main removes it at exit).
func buildSuper() (string, string) {
	root, err := os.MkdirTemp(os.Getenv("HOME"), "gittest-super-")
	if err != nil {
		return "", err.Error()
	}
	var fail string
	run := func(dir string, args ...string) {
		if fail != "" {
			return
		}
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			fail = "git " + strings.Join(args, " ") + ": " + err.Error() + "\n" + string(out)
		}
	}
	write := func(path, content string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o644)
	}
	bare := func(name, sub string) {
		work := filepath.Join(root, name+"-src")
		run(root, "init", "-q", "-b", "main", work)
		write(filepath.Join(work, name+".txt"), name+"\n")
		run(work, "add", ".")
		run(work, "commit", "-qm", name)
		if sub != "" {
			run(work, "submodule", "add", "-q", "../"+sub+".git", sub)
			run(work, "commit", "-qm", "add "+sub)
		}
		run(root, "clone", "-q", "--bare", work, filepath.Join(root, name+".git"))
	}
	bare("deep", "")
	bare("x", "deep")
	super := filepath.Join(root, "super")
	run(root, "init", "-q", "-b", "main", super)
	write(filepath.Join(super, "top.txt"), "top\n")
	run(super, "add", ".")
	run(super, "commit", "-qm", "top")
	run(super, "submodule", "add", "-q", "../x.git", "libs/a")
	run(super, "submodule", "update", "-q", "--init", "--recursive")
	run(super, "clone", "-q", "../x.git", "libs/inl")
	run(super, "submodule", "add", "-q", "../x.git", "libs/inl")
	run(super, "init", "-q", "-b", "main", "libs/emb")
	write(filepath.Join(super, "libs/emb/e.txt"), "e\n")
	run(filepath.Join(super, "libs/emb"), "add", ".")
	run(filepath.Join(super, "libs/emb"), "commit", "-qm", "e")
	run(super, "add", "libs/emb")
	run(super, "submodule", "add", "-q", "../x.git", "libs/u")
	run(super, "commit", "-qm", "submodules")
	run(super, "submodule", "deinit", "-q", "-f", "libs/u")
	return root, fail
}
