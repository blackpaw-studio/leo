package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// gitEnv returns an environment with deterministic committer/author identity
// so `git commit` works on CI machines without a configured user.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
}

// setupScratchRepo creates a bare origin repo with main + feat/existing, then
// clones it to a working path. Returns (clonePath, originPath).
func setupScratchRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	originPath := filepath.Join(tmp, "origin.git")
	seedPath := filepath.Join(tmp, "seed")
	clonePath := filepath.Join(tmp, "clone")

	mustRun := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	if err := os.MkdirAll(seedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(seedPath, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seedPath, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(seedPath, "add", ".")
	mustRun(seedPath, "commit", "-m", "initial")

	mustRun(seedPath, "checkout", "-b", "feat/existing")
	if err := os.WriteFile(filepath.Join(seedPath, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(seedPath, "add", ".")
	mustRun(seedPath, "commit", "-m", "feat commit")
	mustRun(seedPath, "checkout", "main")

	initBare := exec.Command("git", "init", "--bare", "-b", "main", originPath)
	initBare.Env = gitEnv()
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	mustRun(seedPath, "remote", "add", "origin", originPath)
	mustRun(seedPath, "push", "origin", "main")
	mustRun(seedPath, "push", "origin", "feat/existing")

	clone := exec.Command("git", "clone", originPath, clonePath)
	clone.Env = gitEnv()
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	return clonePath, originPath
}

// commitInWorktree makes a new commit inside a worktree so the branch has
// unique history (used to test unmerged-branch deletion).
func commitInWorktree(t *testing.T, wt string, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "wip"},
	} {
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestFetch(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	if err := Fetch(testCtx(t), clone); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestDefaultBranch(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	got, err := DefaultBranch(testCtx(t), clone)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Fatalf("got %q want main", got)
	}
}

func TestGetBranchStatus(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)

	s, err := GetBranchStatus(testCtx(t), clone, "main")
	if err != nil {
		t.Fatalf("status main: %v", err)
	}
	if !s.Local || !s.Remote {
		t.Fatalf("main expected local+remote, got %+v", s)
	}

	s, err = GetBranchStatus(testCtx(t), clone, "feat/existing")
	if err != nil {
		t.Fatalf("status feat: %v", err)
	}
	if s.Local {
		t.Fatalf("feat/existing should not be local yet, got %+v", s)
	}
	if !s.Remote {
		t.Fatalf("feat/existing expected remote, got %+v", s)
	}

	s, err = GetBranchStatus(testCtx(t), clone, "no/such/branch")
	if err != nil {
		t.Fatalf("status nonexistent: %v", err)
	}
	if s.Local || s.Remote {
		t.Fatalf("nonexistent expected neither, got %+v", s)
	}
}

func TestAddWorktreeNew(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeNew(testCtx(t), clone, wt, "feat/new", "main"); err != nil {
		t.Fatalf("AddWorktreeNew: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree missing .git: %v", err)
	}
}

func TestAddWorktreeTracking(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeTracking(testCtx(t), clone, wt, "feat/existing"); err != nil {
		t.Fatalf("AddWorktreeTracking: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree missing .git: %v", err)
	}
}

func TestAddWorktreeExisting(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	if _, err := ExecGit(testCtx(t), clone, "branch", "local-only", "main"); err != nil {
		t.Fatalf("create local branch: %v", err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeExisting(testCtx(t), clone, wt, "local-only"); err != nil {
		t.Fatalf("AddWorktreeExisting: %v", err)
	}
}

func TestAddWorktreeAlreadyCheckedOut(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt1 := filepath.Join(t.TempDir(), "wt1")
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := AddWorktreeNew(testCtx(t), clone, wt1, "dup", "main"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := AddWorktreeExisting(testCtx(t), clone, wt2, "dup")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("expected ErrBranchCheckedOut, got %v", err)
	}
}

func TestRemoveWorktreeClean(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeNew(testCtx(t), clone, wt, "to-remove", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := RemoveWorktree(testCtx(t), clone, wt, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree gone, stat err=%v", err)
	}
}

func TestRemoveWorktreeDirty(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeNew(testCtx(t), clone, wt, "dirty-branch", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RemoveWorktree(testCtx(t), clone, wt, false)
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("expected ErrWorktreeDirty, got %v", err)
	}
	if err := RemoveWorktree(testCtx(t), clone, wt, true); err != nil {
		t.Fatalf("force remove: %v", err)
	}
}

func TestDeleteBranchMerged(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	if _, err := ExecGit(testCtx(t), clone, "branch", "merged-branch", "main"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DeleteBranch(testCtx(t), clone, "merged-branch", false); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestDeleteBranchUnmerged(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeNew(testCtx(t), clone, wt, "unmerged", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commitInWorktree(t, wt, "new.txt", "hello\n")
	if err := RemoveWorktree(testCtx(t), clone, wt, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	err := DeleteBranch(testCtx(t), clone, "unmerged", false)
	if !errors.Is(err, ErrBranchNotMerged) {
		t.Fatalf("expected ErrBranchNotMerged, got %v", err)
	}
	if err := DeleteBranch(testCtx(t), clone, "unmerged", true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
}

func TestDeleteBranchMissing(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	err := DeleteBranch(testCtx(t), clone, "never-existed", false)
	if !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("expected ErrBranchNotFound, got %v", err)
	}
}

func TestPruneWorktrees(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktreeNew(testCtx(t), clone, wt, "prunable", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := PruneWorktrees(testCtx(t), clone); err != nil {
		t.Fatalf("prune: %v", err)
	}
}

func TestIsDirty(t *testing.T) {
	t.Parallel()
	clone, _ := setupScratchRepo(t)
	dirty, err := IsDirty(testCtx(t), clone)
	if err != nil {
		t.Fatalf("IsDirty clean: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean")
	}
	if err := os.WriteFile(filepath.Join(clone, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = IsDirty(testCtx(t), clone)
	if err != nil {
		t.Fatalf("IsDirty dirty: %v", err)
	}
	if !dirty {
		t.Fatalf("expected dirty")
	}
}
