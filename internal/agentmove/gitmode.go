package agentmove

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/git"
)

// Git modes. In plain mode the workspace's .git travels inside the workspace
// rsync; main and linked never rsync a git dir: refs and commits are pushed,
// the index travels as a tree, and the destination worktree is rebuilt.
const (
	GitModePlain  = "plain"
	GitModeMain   = "main"
	GitModeLinked = "linked"
)

// GitInfo is a source workspace's git state as a move sees it. Only IsRepo
// and Mode are set in plain mode.
type GitInfo struct {
	// IsRepo is set when the workspace has its own .git.
	IsRepo bool
	Mode   string
	// Canonical is the repo's main worktree: the record's CanonicalPath
	// when it has one.
	Canonical string
	CommonDir string
	// GitDir is the workspace's own git dir (a linked worktree's admin dir).
	GitDir   string
	HeadRef  string
	HeadSHA  string
	RootSHAs []string
	// ModulesDir is the workspace's private submodule gitdir root.
	ModulesDir string
	Submodules []git.Submodule
	// NestedWorktrees are the repo's other worktrees inside the workspace,
	// relative to it: they are skipped with a note.
	NestedWorktrees []string
}

// DetectGit classifies ws (run on the source at P0): .git a file is linked;
// a .git dir whose repo has other worktrees is main; anything else is plain.
// recCanonical, the record's CanonicalPath, must name the workspace's repo
// when set.
func DetectGit(ctx context.Context, ws, recCanonical string) (GitInfo, error) {
	dotGit, err := os.Lstat(filepath.Join(ws, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return GitInfo{Mode: GitModePlain}, nil
	}
	if err != nil {
		return GitInfo{}, fmt.Errorf("inspecting %s/.git: %w", ws, err)
	}
	common, err := git.CommonDir(ctx, ws)
	if err != nil {
		return GitInfo{}, err
	}
	if filepath.Base(common) != ".git" {
		return GitInfo{}, problemf(CodeGitUnsupported, "the repository's git dir %s is not <repo>/.git", common)
	}
	canonical := filepath.Dir(common)
	if recCanonical != "" {
		if !git.SamePath(canonical, recCanonical) {
			return GitInfo{}, problemf(CodeGitUnsupported, "the workspace belongs to %s, but the agent's record names canonical %s", canonical, recCanonical)
		}
		canonical = recCanonical
	}
	list, err := git.WorktreeList(ctx, ws)
	if err != nil {
		return GitInfo{}, err
	}
	g := GitInfo{IsRepo: true, Mode: GitModePlain, CommonDir: common}
	switch {
	case !dotGit.IsDir():
		g.Mode = GitModeLinked
	case len(list) > 1:
		g.Mode = GitModeMain
		if recCanonical == "" {
			canonical = ws
		}
	default:
		return g, nil
	}
	g.Canonical = canonical
	g.NestedWorktrees = nestedWorktrees(ws, list)
	return scanGitMode(ctx, ws, g)
}

// scanGitMode fills in what main and linked mode carry.
func scanGitMode(ctx context.Context, ws string, g GitInfo) (GitInfo, error) {
	var err error
	if g.GitDir, err = git.AbsoluteGitDir(ctx, ws); err != nil {
		return GitInfo{}, err
	}
	head, err := git.HeadState(ctx, ws)
	if err != nil {
		return GitInfo{}, problemf(CodeGitUnsupported, "reading HEAD: %v", err)
	}
	g.HeadRef, g.HeadSHA = head.Ref, head.SHA
	if g.RootSHAs, err = git.RootCommits(ctx, ws); err != nil {
		return GitInfo{}, err
	}
	if g.ModulesDir, err = git.ModulesDir(ctx, ws); err != nil {
		return GitInfo{}, err
	}
	if g.Submodules, err = git.Submodules(ctx, ws); err != nil {
		return GitInfo{}, problemf(CodeGitUnsupported, "scanning submodules: %v", err)
	}
	return g, nil
}

// nestedWorktrees lists the entries of list strictly inside ws, relative.
func nestedWorktrees(ws string, list []git.Worktree) []string {
	root := realPath(ws)
	var out []string
	for _, w := range list {
		if w.Bare {
			continue
		}
		if rel, ok := under(root, realPath(w.Path)); ok {
			out = append(out, rel)
		}
	}
	return out
}

// under returns p relative to root when p is strictly inside it.
func under(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// opNames names the operation each in-progress marker belongs to.
var opNames = map[string]string{
	"MERGE_HEAD": "merge", "CHERRY_PICK_HEAD": "cherry-pick", "REVERT_HEAD": "revert",
	"BISECT_LOG": "bisect", "rebase-merge": "rebase", "rebase-apply": "rebase",
}

// CheckGitMode returns every reason a main or linked workspace cannot move
// in git mode, nil when it can. Nothing has been stopped when it runs.
func CheckGitMode(ctx context.Context, ws string, g GitInfo) error {
	var ps Problems
	add := func(p *Problem) {
		if p != nil {
			ps = append(ps, *p)
		}
	}
	if out, _ := git.ExecGit(ctx, ws, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(string(out)) == "true" {
		add(problemf(CodeGitUnsupported, "the repository is a shallow clone; run `git fetch --unshallow` first"))
	}
	if out, _ := git.ExecGit(ctx, ws, "config", "--type=bool", "--get", "core.sparseCheckout"); strings.TrimSpace(string(out)) == "true" {
		add(problemf(CodeGitUnsupported, "the workspace uses a sparse checkout"))
	}
	marker, err := git.OpInProgress(ctx, ws)
	if err != nil {
		return err
	}
	if marker != "" {
		add(problemf(CodeGitOpInProgress, "a %s is in progress in %s; finish or abort it first", opNames[marker], ws))
	}
	add(asProblem(checkModulesDir(g.Mode, g.ModulesDir, g.CommonDir, g.GitDir)))
	for _, s := range g.Submodules {
		add(asProblem(checkSubmodule(s, g.ModulesDir)))
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}

func asProblem(err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	return nil
}

// checkModulesDir refuses a git whose submodule dirs are shared across
// worktrees: modules must be <gitdir>/modules, and a linked worktree's
// gitdir must be <common>/worktrees/<id>.
func checkModulesDir(mode, modulesDir, commonDir, gitDir string) error {
	if !git.SamePath(modulesDir, filepath.Join(gitDir, "modules")) {
		return problemf(CodeGitUnsupported, "submodule git dirs resolve to %s, shared across worktrees; this git version is unsupported", modulesDir)
	}
	if mode == GitModeLinked && !git.SamePath(filepath.Dir(gitDir), filepath.Join(commonDir, "worktrees")) {
		return problemf(CodeGitUnsupported, "the worktree's git dir %s is not under %s/worktrees", gitDir, commonDir)
	}
	return nil
}

// checkSubmodule refuses a submodule the git-mode transfer cannot carry.
func checkSubmodule(s git.Submodule, modulesDir string) error {
	for _, tok := range []string{s.Path, s.Name} {
		if tok == "" {
			continue
		}
		if err := validateSubmoduleToken(tok); err != nil {
			return err
		}
	}
	if s.State != git.SubmoduleAbsorbed {
		return nil
	}
	if _, ok := under(realPath(modulesDir), realPath(s.GitDir)); !ok {
		return problemf(CodeGitUnsupported, "submodule %s keeps its git dir %s outside %s", s.Path, s.GitDir, modulesDir)
	}
	if entries, err := os.ReadDir(filepath.Join(s.GitDir, "worktrees")); err == nil && len(entries) > 0 {
		return problemf(CodeGitUnsupported, "submodule %s has worktrees of its own (%s/worktrees)", s.Path, s.GitDir)
	}
	if _, err := os.Stat(filepath.Join(s.GitDir, "objects", "info", "alternates")); err == nil {
		return problemf(CodeGitUnsupported, "submodule %s borrows objects through objects/info/alternates", s.Path)
	}
	return nil
}

// submoduleToken is the charset a submodule path or name may use. A leading
// "-" is refused too: these reach git argv.
var submoduleToken = regexp.MustCompile(`^[A-Za-z0-9._@%+=,][A-Za-z0-9._@%+=,/-]*$`)

func validateSubmoduleToken(tok string) error {
	if !submoduleToken.MatchString(tok) {
		return problemf(CodeUnsafePath, "submodule path or name %q uses characters outside [A-Za-z0-9._@%%+=,/-]", tok)
	}
	for _, part := range strings.Split(tok, "/") {
		if part == ".." {
			return problemf(CodeUnsafePath, "submodule path or name %q has a .. component", tok)
		}
	}
	return nil
}

// Notes are the non-fatal remarks detection makes.
func (g GitInfo) Notes() []Problem {
	out := make([]Problem, 0, len(g.NestedWorktrees))
	for _, p := range g.NestedWorktrees {
		out = append(out, Problem{Code: NoteNestedWorktreeSkip, Message: fmt.Sprintf("the worktree at %s inside the workspace is not moved", p)})
	}
	return out
}

// WorkspaceSpec is the workspace rsync's spec for this git state.
func (g GitInfo) WorkspaceSpec(ws string, userExcludes, nested []string) WorkspaceSpec {
	subs := make([]Submodule, len(g.Submodules))
	for i, s := range g.Submodules {
		subs[i] = Submodule{Path: s.Path, State: s.State}
	}
	return WorkspaceSpec{
		Workspace:       ws,
		UserExcludes:    userExcludes,
		Nested:          nested,
		GitMode:         g.Mode != GitModePlain,
		Submodules:      subs,
		NestedWorktrees: g.NestedWorktrees,
	}
}

// Manifest is the git block a transfer sends for this state: submodule
// gitdirs relative to the modules dir, and the branch's upstream. repo is
// owner/repo when the destination may clone a missing canonical with gh.
// IndexCommit is filled by G1.
func (g GitInfo) Manifest(ctx context.Context, ws, repo string) (*GitManifest, error) {
	if !g.IsRepo {
		return nil, nil
	}
	m := &GitManifest{
		Mode: g.Mode, Canonical: g.Canonical, HeadRef: g.HeadRef, HeadSHA: g.HeadSHA,
		RootSHAs: g.RootSHAs, Repo: repo, ModulesDir: g.ModulesDir,
	}
	if m.Canonical == "" {
		m.Canonical = ws
	}
	for _, s := range g.Submodules {
		if s.GitDir != "" {
			rel, ok := under(realPath(g.ModulesDir), realPath(s.GitDir))
			if !ok {
				return nil, problemf(CodeGitUnsupported, "submodule %s keeps its git dir %s outside %s", s.Path, s.GitDir, g.ModulesDir)
			}
			s.GitDir = rel
		}
		m.Submodules = append(m.Submodules, s)
	}
	if b := strings.TrimPrefix(g.HeadRef, "refs/heads/"); b != "" {
		if remote := git.ConfigGet(ctx, ws, "branch."+b+".remote"); remote != "" {
			m.Upstream = &Upstream{Remote: remote, Merge: git.ConfigGet(ctx, ws, "branch."+b+".merge")}
		}
	}
	return m, nil
}
