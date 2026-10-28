package agentmove

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/git"
)

// Kinds of NestedPath.
const (
	NestedAgent    = "agent"
	NestedWorktree = "worktree"
	NestedRepo     = "repo"
)

// NestedPath is something the destination holds inside the target
// workspace: another agent's workspace, a linked worktree of the repo, or a
// nested repository. Path is relative to the workspace.
type NestedPath struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// nestedPaths finds them under ws (run on the destination at preflight),
// sorted by path; a path found as more than one kind keeps the first of
// agent, worktree, repo.
func nestedPaths(ctx context.Context, ws, name string, agents []agent.Record) []NestedPath {
	if !isDir(ws) {
		return nil
	}
	root := realPath(ws)
	seen := map[string]bool{}
	var out []NestedPath
	add := func(p, kind string) {
		if rel, ok := under(root, realPath(p)); ok && !seen[rel] {
			seen[rel] = true
			out = append(out, NestedPath{Path: rel, Kind: kind})
		}
	}
	for _, a := range agents {
		if a.Name != name && a.Workspace != "" {
			add(a.Workspace, NestedAgent)
		}
	}
	if list, err := git.WorktreeList(ctx, ws); err == nil {
		for _, w := range list {
			if !w.Bare {
				add(w.Path, NestedWorktree)
			}
		}
	}
	for _, rel := range nestedRepos(root) {
		add(filepath.Join(root, rel), NestedRepo)
	}
	slices.SortFunc(out, func(a, b NestedPath) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// nestedRepos lists the directories under root holding a .git entry (a
// repository or a linked worktree), relative, without descending into them
// or into root's own .git. Unreadable directories are skipped.
func nestedRepos(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if _, err := os.Lstat(filepath.Join(p, ".git")); err != nil {
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			out = append(out, rel)
		}
		return filepath.SkipDir
	})
	return out
}

// protectedNested picks the destination's nested paths the workspace rsync
// must exclude, which keeps --delete off them: all of them, except a
// repository that ws also has at that path or one of ws's submodules, which
// the source's copy replaces. kept are the protected paths ws also has
// files at: those files are not sent.
func protectedNested(ws string, nested []NestedPath, subs []git.Submodule) (protect []string, kept []NestedPath) {
	for _, n := range nested {
		rel := strings.Trim(filepath.Clean(n.Path), "/")
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if n.Kind == NestedRepo && (exists(filepath.Join(ws, rel, ".git")) ||
			slices.ContainsFunc(subs, func(s git.Submodule) bool { return s.Path == rel })) {
			continue
		}
		protect = append(protect, rel)
		if exists(filepath.Join(ws, rel)) {
			kept = append(kept, NestedPath{Path: rel, Kind: n.Kind})
		}
	}
	return protect, kept
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

var nestedLabels = map[string]string{NestedAgent: "another agent's workspace", NestedWorktree: "a worktree", NestedRepo: "a repository"}

// keptNote says what a kept path held back.
func keptNote(n NestedPath, host string) Problem {
	label := nestedLabels[n.Kind]
	if label == "" {
		label = "something of its own"
	}
	return Problem{Code: NoteDestNestedKept, Message: fmt.Sprintf("%s holds %s at %s inside the workspace; it is kept, and this host's files there are not sent", host, label, n.Path)}
}
