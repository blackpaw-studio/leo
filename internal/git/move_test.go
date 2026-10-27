package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/git/gittest"
)

var (
	run              = gittest.Run
	write            = gittest.Write
	newRepo          = gittest.NewRepo
	status           = gittest.Status
	copyWorkingFiles = gittest.CopyWorkingFiles
)

// TestSnapshotRoundTripPreservesStatus repeats the git-mode prototype: a
// linked worktree with an unpushed commit, a staged hunk, an unstaged edit,
// an untracked and an ignored file arrives in another clone's new worktree
// with an identical status.
func TestSnapshotRoundTripPreservesStatus(t *testing.T) {
	ctx := context.Background()
	src := newRepo(t)
	srcWT := filepath.Join(t.TempDir(), "wt")
	run(t, src, "worktree", "add", "-q", "-b", "move-wt", srcWT)
	write(t, filepath.Join(srcWT, "c.txt"), "unpushed\n")
	run(t, srcWT, "add", "c.txt")
	run(t, srcWT, "commit", "-qm", "unpushed")
	write(t, filepath.Join(srcWT, "a.txt"), "a\nstaged\n")
	run(t, srcWT, "add", "a.txt")
	write(t, filepath.Join(srcWT, "a.txt"), "a\nstaged\nunstaged\n")
	write(t, filepath.Join(srcWT, "b.txt"), "b\nunstaged\n")
	write(t, filepath.Join(srcWT, "new.txt"), "untracked\n")
	write(t, filepath.Join(srcWT, "ignored.log"), "ignored\n")
	before := status(t, srcWT)

	head, err := HeadState(ctx, srcWT)
	if err != nil || head.Ref != "refs/heads/move-wt" || head.SHA == "" {
		t.Fatalf("HeadState = %+v, %v", head, err)
	}
	if _, err := SnapshotIndex(ctx, srcWT, "refs/leo-move/a/index"); err != nil {
		t.Fatalf("SnapshotIndex: %v", err)
	}
	if status(t, srcWT) != before {
		t.Fatal("snapshot changed the source's status")
	}

	dst := filepath.Join(t.TempDir(), "dst")
	run(t, filepath.Dir(dst), "clone", "-q", src, dst)
	run(t, srcWT, "push", "-q", dst, "+"+head.SHA+":refs/leo-move/a/head", "+refs/leo-move/a/index:refs/leo-move/a/index")

	if err := UpdateRefCAS(ctx, dst, "refs/heads/move-wt", head.SHA, ""); err != nil {
		t.Fatalf("UpdateRefCAS: %v", err)
	}
	dstWT := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAddNoCheckout(ctx, dst, dstWT, "move-wt", ""); err != nil {
		t.Fatalf("WorktreeAddNoCheckout: %v", err)
	}
	copyWorkingFiles(t, srcWT, dstWT)
	if err := ReadTree(ctx, dstWT, "refs/leo-move/a/index^{tree}"); err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if err := RefreshIndex(ctx, dstWT); err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}
	if after := status(t, dstWT); after != before {
		t.Fatalf("status differs\nsource:\n%s\ndestination:\n%s", before, after)
	}
	if err := DeleteRefs(ctx, dst, "refs/leo-move/a/head", "refs/leo-move/a/index"); err != nil {
		t.Fatalf("DeleteRefs: %v", err)
	}
	if out, _ := ExecGit(ctx, dst, "for-each-ref", "refs/leo-move/"); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("move refs left: %s", out)
	}
}

func TestUpdateRefCASRefusesStaleOld(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	first := run(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.txt"), "two\n")
	run(t, dir, "commit", "-qam", "two")
	second := run(t, dir, "rev-parse", "HEAD")

	if err := UpdateRefCAS(ctx, dir, "refs/heads/x", first, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := UpdateRefCAS(ctx, dir, "refs/heads/x", second, ""); err == nil {
		t.Fatal("create over an existing ref succeeded")
	}
	if err := UpdateRefCAS(ctx, dir, "refs/heads/x", second, second); err == nil {
		t.Fatal("update with a stale old value succeeded")
	}
	if got := run(t, dir, "rev-parse", "refs/heads/x"); got != first {
		t.Fatalf("ref moved to %s after refused updates", got)
	}
	if err := UpdateRefCAS(ctx, dir, "refs/heads/x", second, first); err != nil {
		t.Fatalf("CAS with the right old value: %v", err)
	}
	if ok, err := IsAncestor(ctx, dir, first, second); err != nil || !ok {
		t.Fatalf("IsAncestor(first, second) = %v, %v", ok, err)
	}
	if ok, err := IsAncestor(ctx, dir, second, first); err != nil || ok {
		t.Fatalf("IsAncestor(second, first) = %v, %v", ok, err)
	}
	roots, err := RootCommits(ctx, dir)
	if err != nil || len(roots) != 1 || roots[0] != first {
		t.Fatalf("RootCommits = %v, %v; want [%s]", roots, err, first)
	}
	if !HasCommit(ctx, dir, second) || HasCommit(ctx, dir, strings.Repeat("1", 40)) {
		t.Fatal("HasCommit wrong")
	}
}

func TestOpInProgressDetectsRebaseAndMerge(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if op, err := OpInProgress(ctx, dir); err != nil || op != "" {
		t.Fatalf("clean repo: op = %q, %v", op, err)
	}
	run(t, dir, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(dir, "a.txt"), "other\n")
	run(t, dir, "commit", "-qam", "other")
	run(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	run(t, dir, "commit", "-qam", "main")
	_ = exec.Command("git", "-C", dir, "merge", "other").Run()
	if op, _ := OpInProgress(ctx, dir); op != "MERGE_HEAD" {
		t.Fatalf("mid-merge op = %q, want MERGE_HEAD", op)
	}
	run(t, dir, "merge", "--abort")
	_ = exec.Command("git", "-C", dir, "rebase", "other").Run()
	if op, _ := OpInProgress(ctx, dir); op != "rebase-merge" && op != "rebase-apply" {
		t.Fatalf("mid-rebase op = %q, want rebase-merge or rebase-apply", op)
	}
}

func TestWorktreeListReportsCheckedOutBranches(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "feat", wt)
	det := filepath.Join(t.TempDir(), "det")
	run(t, dir, "worktree", "add", "-q", "--detach", det)

	list, err := WorktreeList(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("WorktreeList = %+v, want 3 entries", list)
	}
	byBranch := CheckedOut(list)
	if !SamePath(byBranch["main"], dir) || !SamePath(byBranch["feat"], wt) || len(byBranch) != 2 {
		t.Fatalf("CheckedOut = %v", byBranch)
	}
}
