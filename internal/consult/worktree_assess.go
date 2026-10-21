package consult

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// worktreeVerdict is what the safety rules decide for one dispatch worktree.
// Cleanup and `leo dispatch prune` both ask assessWorktree, so they can never
// disagree about what is safe to delete.
type worktreeVerdict string

const (
	// verdictRemove: clean, and either still at the base commit or with every
	// commit reachable from a ref other than its own branch.
	verdictRemove worktreeVerdict = "remove"
	// verdictDirty: modified tracked files or untracked (non-ignored) files.
	verdictDirty worktreeVerdict = "keep:dirty"
	// verdictUnmerged: clean, but commits no other ref reaches.
	verdictUnmerged worktreeVerdict = "keep:unmerged"
	// verdictUnverifiable: the rules could not be applied (git failed, the
	// worktree is locked, on another branch, or unregistered).
	verdictUnverifiable worktreeVerdict = "keep:unverifiable"
	// verdictMissingRepo: the canonical repository is gone, so nothing can
	// be checked or removed through it.
	verdictMissingRepo worktreeVerdict = "missing-repo"
	// verdictGone: the worktree directory no longer exists.
	verdictGone worktreeVerdict = "gone"
)

// gitFunc runs git with args and returns its trimmed output.
type gitFunc func(args ...string) ([]byte, error)

type worktreeAssessment struct {
	Verdict worktreeVerdict
	Reason  string
}

func assessed(v worktreeVerdict, format string, args ...any) worktreeAssessment {
	return worktreeAssessment{Verdict: v, Reason: fmt.Sprintf(format, args...)}
}

// registeredWorktree is one entry of `git worktree list --porcelain`.
type registeredWorktree struct {
	path, head, branch string
	locked             bool
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// findRegisteredWorktree looks path up in the canonical repository's own
// worktree list, so HEAD and branch come from the repository that owns the
// worktree rather than from whatever the directory claims.
func findRegisteredWorktree(git gitFunc, root, path string) (registeredWorktree, bool, error) {
	out, err := git("-C", root, "worktree", "list", "--porcelain")
	if err != nil {
		return registeredWorktree{}, false, err
	}
	want := canonicalPath(path)
	var current registeredWorktree
	flush := func() (registeredWorktree, bool) {
		if current.path != "" && canonicalPath(current.path) == want {
			return current, true
		}
		return registeredWorktree{}, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if entry, ok := flush(); ok {
				return entry, true, nil
			}
			current = registeredWorktree{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.branch = strings.TrimPrefix(line, "branch ")
		case line == "locked" || strings.HasPrefix(line, "locked "):
			current.locked = true
		}
	}
	entry, ok := flush()
	return entry, ok, nil
}

// refsReaching lists the refs, other than refs/heads/<ownBranch>, whose
// history contains commit.
func refsReaching(git gitFunc, root, commit, ownBranch string) ([]string, error) {
	out, err := git("-C", root, "for-each-ref", "--contains", commit, "--format=%(refname)")
	if err != nil {
		return nil, err
	}
	own := "refs/heads/" + ownBranch
	var refs []string
	for _, ref := range strings.Fields(string(out)) {
		if ref != own {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// assessWorktree applies the safety rules to rec's worktree without changing
// anything. Git runs against the dispatch's canonical repository
// (RepositoryRoot); only the status check, which is inherently per-checkout,
// runs inside the worktree.
func assessWorktree(git gitFunc, rec Record) worktreeAssessment {
	if rec.Worktree == "" || rec.RepositoryRoot == "" || rec.BaseCommit == "" || rec.Branch == "" {
		return assessed(verdictUnverifiable, "record lacks worktree metadata")
	}
	if info, err := os.Stat(rec.RepositoryRoot); err != nil || !info.IsDir() {
		return assessed(verdictMissingRepo, "repository %s is gone", rec.RepositoryRoot)
	}
	if _, err := os.Lstat(rec.Worktree); os.IsNotExist(err) {
		return assessed(verdictGone, "worktree directory is gone")
	}
	entry, found, err := findRegisteredWorktree(git, rec.RepositoryRoot, rec.Worktree)
	switch {
	case err != nil:
		return assessed(verdictUnverifiable, "listing worktrees: %v", err)
	case !found:
		return assessed(verdictUnverifiable, "worktree is not registered with %s", rec.RepositoryRoot)
	case entry.locked:
		return assessed(verdictUnverifiable, "worktree is locked")
	case entry.branch != "refs/heads/"+rec.Branch:
		return assessed(verdictUnverifiable, "worktree is on %q, not %s", entry.branch, rec.Branch)
	}
	status, err := git("-C", rec.Worktree, "status", "--porcelain")
	switch {
	case err != nil:
		return assessed(verdictUnverifiable, "git status: %v", err)
	case len(status) != 0:
		return assessed(verdictDirty, "uncommitted changes")
	}
	if entry.head == rec.BaseCommit {
		return assessed(verdictRemove, "clean at base commit")
	}
	refs, err := refsReaching(git, rec.RepositoryRoot, entry.head, rec.Branch)
	switch {
	case err != nil:
		return assessed(verdictUnverifiable, "checking reachability: %v", err)
	case len(refs) == 0:
		return assessed(verdictUnmerged, "commits not reachable from any other ref")
	}
	return assessed(verdictRemove, "clean, commits reachable from %s", refs[0])
}

// removeWorktreeCheckout removes the checkout through the canonical
// repository. It never forces: git itself refuses a dirty or locked one, and
// ignored build output does not count as dirty.
func removeWorktreeCheckout(git gitFunc, rec Record) error {
	_, err := git("-C", rec.RepositoryRoot, "worktree", "remove", rec.Worktree)
	return err
}

// deleteMergedBranch deletes rec.Branch only when its tip is reachable from
// another ref, and returns that tip. An absent branch or any git failure
// leaves it alone.
func deleteMergedBranch(git gitFunc, rec Record) (tip string, deleted bool) {
	out, err := git("-C", rec.RepositoryRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+rec.Branch+"^{commit}")
	if err != nil {
		return "", false
	}
	tip = string(out)
	refs, err := refsReaching(git, rec.RepositoryRoot, tip, rec.Branch)
	if err != nil || len(refs) == 0 {
		return "", false
	}
	if _, err := git("-C", rec.RepositoryRoot, "branch", "-D", rec.Branch); err != nil {
		return "", false
	}
	return tip, true
}

// pruneRepositoryWorktrees drops git's stale registrations in root.
func pruneRepositoryWorktrees(git gitFunc, root string) error {
	_, err := git("-C", root, "worktree", "prune")
	return err
}
