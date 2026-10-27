package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// zeroOID is the all-zero object id: as update-ref's old value it means
// "the ref must not exist yet".
const zeroOID = "0000000000000000000000000000000000000000"

// gitOut runs git in dir and returns trimmed stdout+stderr, wrapping a
// failure with the command and its output.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := ExecGit(ctx, dir, args...)
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// exitCode is err's process exit code, -1 when it is not an exit.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Head is what HEAD points at.
type Head struct {
	// Ref is the full branch ref, "" when HEAD is detached.
	Ref string
	SHA string
}

// Branch is Ref without refs/heads/.
func (h Head) Branch() string { return strings.TrimPrefix(h.Ref, "refs/heads/") }

// HeadState reads HEAD's ref (symbolic-ref -q) and commit.
func HeadState(ctx context.Context, dir string) (Head, error) {
	sha, err := gitOut(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Head{}, err
	}
	out, err := ExecGit(ctx, dir, "symbolic-ref", "-q", "HEAD")
	if err != nil && exitCode(err) != 1 {
		return Head{}, fmt.Errorf("git symbolic-ref -q HEAD: %w: %s", err, out)
	}
	ref := ""
	if err == nil {
		ref = strings.TrimSpace(string(out))
	}
	return Head{Ref: ref, SHA: sha}, nil
}

// SnapshotIndex records dir's index as a commit on top of HEAD and points
// ref at it (G1): write-tree, commit-tree -p HEAD, update-ref. The worktree
// itself is only read.
func SnapshotIndex(ctx context.Context, dir, ref string) (string, error) {
	tree, err := gitOut(ctx, dir, "write-tree")
	if err != nil {
		return "", err
	}
	commit, err := gitOut(ctx, dir, "commit-tree", tree, "-p", "HEAD", "-m", "leo-move")
	if err != nil {
		return "", err
	}
	if _, err := gitOut(ctx, dir, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func IsAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	out, err := ExecGit(ctx, dir, "merge-base", "--is-ancestor", a, b)
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w: %s", err, strings.TrimSpace(string(out)))
}

// UpdateRefCAS points ref at newSHA only if it currently holds oldSHA; an
// empty oldSHA means the ref must not exist.
func UpdateRefCAS(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	if oldSHA == "" {
		oldSHA = zeroOID
	}
	_, err := gitOut(ctx, dir, "update-ref", ref, newSHA, oldSHA)
	return err
}

// UpdateRef points ref at sha unconditionally.
func UpdateRef(ctx context.Context, dir, ref, sha string) error {
	_, err := gitOut(ctx, dir, "update-ref", ref, sha)
	return err
}

// WorktreeAddNoCheckout adds a worktree at path without touching its files:
// on branch when set, else detached at detachSHA.
func WorktreeAddNoCheckout(ctx context.Context, repo, path, branch, detachSHA string) error {
	if branch != "" {
		return runWorktreeAdd(ctx, repo, "worktree", "add", "--no-checkout", path, branch)
	}
	return runWorktreeAdd(ctx, repo, "worktree", "add", "--no-checkout", "--detach", path, detachSHA)
}

// WorktreeAddNewBranchNoCheckout adds a worktree at path on a new branch
// created at startSHA, without touching its files.
func WorktreeAddNewBranchNoCheckout(ctx context.Context, repo, path, branch, startSHA string) error {
	return runWorktreeAdd(ctx, repo, "worktree", "add", "--no-checkout", "-b", branch, path, startSHA)
}

// WorktreeMove renames a linked worktree; its admin id survives.
func WorktreeMove(ctx context.Context, repo, from, to string) error {
	_, err := gitOut(ctx, repo, "worktree", "move", from, to)
	return err
}

// ReadTree replaces dir's index with treeish.
func ReadTree(ctx context.Context, dir, treeish string) error {
	_, err := gitOut(ctx, dir, "read-tree", treeish)
	return err
}

// RefreshIndex refreshes the index's stat info against the working files.
func RefreshIndex(ctx context.Context, dir string) error {
	_, err := gitOut(ctx, dir, "update-index", "-q", "--refresh")
	return err
}

// SetHEAD points dir's HEAD at ref (a branch), or detaches it at sha when
// ref is empty. The index and files are left alone.
func SetHEAD(ctx context.Context, dir, ref, sha string) error {
	if ref != "" {
		_, err := gitOut(ctx, dir, "symbolic-ref", "HEAD", ref)
		return err
	}
	_, err := gitOut(ctx, dir, "update-ref", "--no-deref", "HEAD", sha)
	return err
}

// Worktree is one `git worktree list --porcelain` entry.
type Worktree struct {
	Path   string
	Head   string
	Branch string // full ref, "" when detached or bare
	Bare   bool
}

// WorktreeList lists every worktree of dir's repository.
func WorktreeList(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := gitOut(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	for _, block := range strings.Split(out, "\n\n") {
		var w Worktree
		for _, line := range strings.Split(block, "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				w.Path = val
			case "HEAD":
				w.Head = val
			case "branch":
				w.Branch = val
			case "bare":
				w.Bare = true
			}
		}
		if w.Path != "" {
			list = append(list, w)
		}
	}
	return list, nil
}

// CheckedOut maps each checked-out branch (short name) to its worktree path.
func CheckedOut(list []Worktree) map[string]string {
	out := map[string]string{}
	for _, w := range list {
		if w.Branch != "" {
			out[strings.TrimPrefix(w.Branch, "refs/heads/")] = w.Path
		}
	}
	return out
}

// SamePath reports whether a and b name the same location once symlinks
// (macOS /var → /private/var) are resolved.
func SamePath(a, b string) bool {
	return resolve(a) == resolve(b)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// inProgressMarkers are the git-path entries an unfinished operation leaves.
var inProgressMarkers = []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"}

// OpInProgress names the git operation dir is in the middle of (its marker
// file), "" when none.
func OpInProgress(ctx context.Context, dir string) (string, error) {
	for _, m := range inProgressMarkers {
		p, err := GitPath(ctx, dir, m)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(p); err == nil {
			return m, nil
		}
	}
	return "", nil
}

// GitPath resolves `git rev-parse --git-path name` to an absolute path.
func GitPath(ctx context.Context, dir, name string) (string, error) {
	return gitOut(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
}

// CommonDir is dir's absolute common git dir.
func CommonDir(ctx context.Context, dir string) (string, error) {
	return gitOut(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// AbsoluteGitDir is dir's own absolute git dir (a linked worktree's admin
// dir, not the common one).
func AbsoluteGitDir(ctx context.Context, dir string) (string, error) {
	return gitOut(ctx, dir, "rev-parse", "--absolute-git-dir")
}

// RootCommits lists HEAD's root commits.
func RootCommits(ctx context.Context, dir string) ([]string, error) {
	out, err := gitOut(ctx, dir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// HasCommit reports whether sha names a commit in dir's repository.
func HasCommit(ctx context.Context, dir, sha string) bool {
	_, err := ExecGit(ctx, dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// RefSHA returns the commit ref points at, "" when it does not exist.
func RefSHA(ctx context.Context, dir, ref string) (string, error) {
	out, err := ExecGit(ctx, dir, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		if exitCode(err) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git rev-parse %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DeleteRefs deletes each ref; a missing one is not an error.
func DeleteRefs(ctx context.Context, dir string, refs ...string) error {
	for _, ref := range refs {
		if _, err := gitOut(ctx, dir, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}
