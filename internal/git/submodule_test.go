package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/git/gittest"
)

var superWithSubmodules = gittest.SuperWithSubmodules

func TestSubmodulesScanStates(t *testing.T) {
	ctx := context.Background()
	super := superWithSubmodules(t)
	subs, err := Submodules(ctx, super)
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	got := map[string]Submodule{}
	for _, s := range subs {
		got[s.Path] = s
	}
	want := map[string]string{"libs/a": "absorbed", "libs/a/deep": "absorbed", "libs/inl": "inline", "libs/emb": "inline", "libs/u": "uninit"}
	if len(got) != len(want) {
		t.Fatalf("Submodules = %+v, want paths %v", subs, want)
	}
	for path, state := range want {
		if got[path].State != state {
			t.Errorf("%s state = %q, want %q", path, got[path].State, state)
		}
	}
	modules, _ := ModulesDir(ctx, super)
	a := got["libs/a"]
	if a.Name != "libs/a" || a.GitDir != filepath.Join(modules, "libs/a") || a.HeadSHA == "" || a.HeadRef != "refs/heads/main" {
		t.Errorf("libs/a = %+v", a)
	}
	if !strings.HasSuffix(a.URL, "x.git") || a.Parent != "" {
		t.Errorf("libs/a url/parent = %q/%q", a.URL, a.Parent)
	}
	deep := got["libs/a/deep"]
	if deep.Parent != "libs/a" || deep.Name != "deep" || !strings.HasPrefix(deep.GitDir, filepath.Join(modules, "libs/a")) {
		t.Errorf("deep = %+v", deep)
	}
	if got["libs/emb"].Name != "" {
		t.Errorf("embedded repo got a name: %+v", got["libs/emb"])
	}
	// Deepest first, for relinking.
	if subs[0].Path != "libs/a/deep" {
		t.Errorf("first = %s, want the deepest", subs[0].Path)
	}
}

// The leoterm shape: an empty .gitmodules and no gitlinks is no submodule.
func TestSubmodulesEmptyGitmodulesNoGitlinks(t *testing.T) {
	dir := newRepo(t)
	write(t, filepath.Join(dir, ".gitmodules"), "")
	run(t, dir, "add", ".gitmodules")
	run(t, dir, "commit", "-qm", "empty gitmodules")
	subs, err := Submodules(context.Background(), dir)
	if err != nil || len(subs) != 0 {
		t.Fatalf("Submodules = %+v, %v; want none", subs, err)
	}
}

func TestModulesDirPerWorktree(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "feat", wt)
	common, _ := CommonDir(ctx, dir)
	if got, _ := ModulesDir(ctx, dir); !SamePath(got, filepath.Join(common, "modules")) && got != filepath.Join(common, "modules") {
		t.Errorf("main ModulesDir = %q", got)
	}
	got, _ := ModulesDir(ctx, wt)
	if want := filepath.Join(common, "worktrees", "wt", "modules"); got != want {
		t.Errorf("linked ModulesDir = %q, want %q", got, want)
	}
}

// TestRelinkSubmoduleAcrossDepth moves a superproject's absorbed submodules
// from the main worktree into a linked one, as a move between the two does:
// the private modules dir is copied, the .git files arrive stale, and only
// after RelinkSubmodule does the linked worktree's status match.
func TestRelinkSubmoduleAcrossDepth(t *testing.T) {
	ctx := context.Background()
	super := superWithSubmodules(t)
	write(t, filepath.Join(super, "libs/a/a-dirty.txt"), "dirty\n")
	want := run(t, super, "status", "--porcelain=v2")
	subs, _ := Submodules(ctx, super)
	srcModules, _ := ModulesDir(ctx, super)

	wt := filepath.Join(t.TempDir(), "wt")
	run(t, super, "worktree", "add", "-q", "--no-checkout", "-b", "linked", wt)
	dstModules, _ := ModulesDir(ctx, wt)
	if err := copyDir(srcModules, dstModules); err != nil {
		t.Fatal(err)
	}
	copyWorkingFiles(t, super, wt)
	run(t, wt, "read-tree", "HEAD")
	if got, _ := ExecGit(ctx, wt, "status", "--porcelain=v2"); string(got) == want+"\n" {
		t.Fatal("status already matches before relinking: the fixture proves nothing")
	}
	for _, s := range subs {
		if s.State != SubmoduleAbsorbed {
			continue
		}
		rel, _ := filepath.Rel(srcModules, s.GitDir)
		if err := RelinkSubmodule(ctx, wt, s.Path, filepath.Join(dstModules, rel)); err != nil {
			t.Fatalf("RelinkSubmodule %s: %v", s.Path, err)
		}
		if err := VerifyHead(ctx, filepath.Join(wt, s.Path)); err != nil {
			t.Fatalf("VerifyHead %s: %v", s.Path, err)
		}
	}
	run(t, wt, "update-index", "-q", "--refresh")
	if got := run(t, wt, "status", "--porcelain=v2"); got != want {
		t.Fatalf("status after relink\n%s\nwant\n%s", got, want)
	}
	gitFile, _ := os.ReadFile(filepath.Join(wt, "libs/a/.git"))
	if strings.Contains(string(gitFile), super) || !strings.HasPrefix(string(gitFile), "gitdir: ") {
		t.Fatalf(".git file = %q, want a relative gitdir", gitFile)
	}
}

func TestSetConfigIfUnsetKeepsExisting(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	set, err := SetConfigIfUnset(ctx, dir, "submodule.a.url", "/first")
	if err != nil || !set {
		t.Fatalf("first set = %v, %v", set, err)
	}
	set, err = SetConfigIfUnset(ctx, dir, "submodule.a.url", "/second")
	if err != nil || set {
		t.Fatalf("second set = %v, %v; want kept", set, err)
	}
	if got := run(t, dir, "config", "--get", "submodule.a.url"); got != "/first" {
		t.Fatalf("url = %q, want /first", got)
	}
}

func TestForkSubmoduleBranchCreateOnly(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	sha := run(t, dir, "rev-parse", "HEAD")
	if err := ForkSubmoduleBranch(ctx, dir, "main-copy", sha); err != nil {
		t.Fatalf("ForkSubmoduleBranch: %v", err)
	}
	if got := run(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main-copy" {
		t.Fatalf("HEAD = %q", got)
	}
	run(t, dir, "checkout", "-q", "main")
	if err := ForkSubmoduleBranch(ctx, dir, "main-copy", sha); err == nil {
		t.Fatal("forking onto an existing branch succeeded")
	}
	if got := run(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("a refused fork moved HEAD to %q", got)
	}
}

var copyDir = gittest.CopyDir
