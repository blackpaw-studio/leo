package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ExecGit is the package-level seam for invoking `git`. It is overridable
// from tests. By default it shells out to the git binary with `-C repoPath`
// prepended so callers don't have to manage cwd.
var ExecGit = func(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repoPath}, args...)
	return exec.CommandContext(ctx, "git", full...).CombinedOutput()
}

var (
	// ErrBranchCheckedOut is returned when git refuses a worktree add because
	// the branch is already checked out elsewhere.
	ErrBranchCheckedOut = errors.New("branch already checked out in another worktree")

	// ErrWorktreeDirty is returned by RemoveWorktree when the worktree has
	// uncommitted changes and force is false.
	ErrWorktreeDirty = errors.New("worktree has uncommitted changes")

	// ErrBranchNotMerged is returned by DeleteBranch when the branch is not
	// fully merged and force is false.
	ErrBranchNotMerged = errors.New("branch is not fully merged")

	// ErrBranchNotFound is returned by DeleteBranch when the branch cannot
	// be resolved.
	ErrBranchNotFound = errors.New("branch not found")
)

// BranchStatus reports whether a branch exists locally, on origin, or both.
type BranchStatus struct {
	Local  bool
	Remote bool
}

// Fetch runs `git fetch --prune origin` in the repository at repoPath.
func Fetch(ctx context.Context, repoPath string) error {
	out, err := ExecGit(ctx, repoPath, "fetch", "--prune", "origin")
	if err != nil {
		return fmt.Errorf("git fetch origin: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DefaultBranch returns the default branch name of origin. It reads
// refs/remotes/origin/HEAD first, then falls back to probing for main and
// master.
func DefaultBranch(ctx context.Context, repoPath string) (string, error) {
	if out, err := ExecGit(ctx, repoPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if idx := strings.IndexByte(ref, '/'); idx >= 0 && idx+1 < len(ref) {
			return ref[idx+1:], nil
		}
		if ref != "" {
			return ref, nil
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := ExecGit(ctx, repoPath, "rev-parse", "--verify", "refs/remotes/origin/"+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("unable to determine default branch for %s", repoPath)
}

// GetBranchStatus reports whether branch exists locally and/or on origin.
func GetBranchStatus(ctx context.Context, repoPath, branch string) (BranchStatus, error) {
	if branch == "" {
		return BranchStatus{}, errors.New("branch name is empty")
	}
	var s BranchStatus
	if _, err := ExecGit(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		s.Local = true
	}
	if _, err := ExecGit(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		s.Remote = true
	}
	return s, nil
}

// AddWorktreeExisting creates a worktree at path that checks out an existing
// local branch.
func AddWorktreeExisting(ctx context.Context, repoPath, path, branch string) error {
	return runWorktreeAdd(ctx, repoPath, "worktree", "add", path, branch)
}

// AddWorktreeTracking creates a worktree at path with a new local branch
// tracking origin/<branch>.
func AddWorktreeTracking(ctx context.Context, repoPath, path, branch string) error {
	return runWorktreeAdd(ctx, repoPath, "worktree", "add", "-b", branch, "--track", path, "origin/"+branch)
}

// AddWorktreeNew creates a worktree at path with a new branch based on baseRef.
func AddWorktreeNew(ctx context.Context, repoPath, path, branch, baseRef string) error {
	return runWorktreeAdd(ctx, repoPath, "worktree", "add", "-b", branch, path, baseRef)
}

func runWorktreeAdd(ctx context.Context, repoPath string, args ...string) error {
	out, err := ExecGit(ctx, repoPath, args...)
	if err == nil {
		return nil
	}
	outStr := strings.TrimSpace(string(out))
	lower := strings.ToLower(outStr)
	if strings.Contains(lower, "already checked out") || strings.Contains(lower, "already used by worktree") {
		return fmt.Errorf("%w: %s", ErrBranchCheckedOut, outStr)
	}
	return fmt.Errorf("git worktree add: %s", outStr)
}

// RemoveWorktree removes the worktree at path. If force is false and the
// worktree is dirty, returns ErrWorktreeDirty.
func RemoveWorktree(ctx context.Context, repoPath, path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = []string{"worktree", "remove", "--force", path}
	}
	out, err := ExecGit(ctx, repoPath, args...)
	if err == nil {
		return nil
	}
	outStr := strings.TrimSpace(string(out))
	lower := strings.ToLower(outStr)
	if !force && (strings.Contains(lower, "contains modified") ||
		strings.Contains(lower, "contains untracked") ||
		strings.Contains(lower, "is dirty")) {
		return fmt.Errorf("%w: %s", ErrWorktreeDirty, outStr)
	}
	return fmt.Errorf("git worktree remove: %s", outStr)
}

// DeleteBranch deletes a local branch. If force is false and the branch is
// not fully merged, returns ErrBranchNotMerged.
func DeleteBranch(ctx context.Context, repoPath, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	out, err := ExecGit(ctx, repoPath, "branch", flag, branch)
	if err == nil {
		return nil
	}
	outStr := strings.TrimSpace(string(out))
	lower := strings.ToLower(outStr)
	if strings.Contains(lower, "not fully merged") {
		return fmt.Errorf("%w: %s", ErrBranchNotMerged, outStr)
	}
	if strings.Contains(lower, "not found") || strings.Contains(lower, "no such branch") {
		return fmt.Errorf("%w: %s", ErrBranchNotFound, outStr)
	}
	return fmt.Errorf("git branch %s %s: %s", flag, branch, outStr)
}

// PruneWorktrees removes administrative data for worktrees whose directories
// no longer exist on disk.
func PruneWorktrees(ctx context.Context, repoPath string) error {
	out, err := ExecGit(ctx, repoPath, "worktree", "prune")
	if err != nil {
		return fmt.Errorf("git worktree prune: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// IsDirty reports whether the worktree at path has uncommitted changes.
func IsDirty(ctx context.Context, worktreePath string) (bool, error) {
	out, err := ExecGit(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %s", strings.TrimSpace(string(out)))
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}
