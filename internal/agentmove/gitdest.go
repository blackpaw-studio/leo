package agentmove

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blackpaw-studio/leo/internal/git"
)

// MoveRef is one of a transfer's temp refs in the canonical: kind is head
// (the source HEAD commit) or index (the source index as a commit on it).
// Push never writes refs/heads/*.
func MoveRef(agent, kind string) string { return "refs/leo-move/" + agent + "/" + kind }

// KeyedLocks serialises G3/G4 per canonical: agents sharing one repository
// never update refs or shared config at the same time.
type KeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Lock takes key's lock and returns its release.
func (k *KeyedLocks) Lock(key string) (unlock func()) {
	key = realPath(key)
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	l, ok := k.locks[key]
	if !ok {
		l = &sync.Mutex{}
		k.locks[key] = l
	}
	k.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// SeedCanonical clones repo (owner/repo) to canonical with gh when it is
// missing (P3): into <canonical>.leo-partial, replacing a failed run's, then
// renamed into place.
func SeedCanonical(ctx context.Context, r Runner, repo, canonical string) error {
	if _, err := os.Stat(canonical); err == nil {
		return nil
	}
	if err := ValidatePath(canonical); err != nil {
		return err
	}
	if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") || strings.HasPrefix(repo, "-") {
		return fmt.Errorf("cannot clone %q: not owner/repo", repo)
	}
	partial := canonical + PartialSuffix
	if err := os.RemoveAll(partial); err != nil {
		return fmt.Errorf("clearing %s: %w", partial, err)
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o750); err != nil {
		return err
	}
	if _, err := r.Run(ctx, Cmd{Name: "gh", Args: []string{"repo", "clone", repo, partial}}); err != nil {
		return fmt.Errorf("cloning %s: %w", repo, err)
	}
	return os.Rename(partial, canonical)
}

// gitTarget is where G3 and G4 act: the canonical, the destination
// workspace (a copy's partial until G4 renames it) and the ref namespace.
func gitTarget(m Manifest) (canonical, ws, name string) {
	ws = m.TargetWorkspace()
	if m.IsCopy() {
		ws += PartialSuffix
	}
	return m.Git.Canonical, ws, m.TargetName()
}

// GitPrepareResult is what G3 tells the source.
type GitPrepareResult struct {
	// ModulesDir is the destination workspace's private modules dir, S5m's
	// target for a move.
	ModulesDir string `json:"modules_dir"`
	// GitDir is the destination workspace's own git dir (a copy's S5m
	// target is under it).
	GitDir string `json:"gitdir"`
}

// GitPrepare (G3) points the target branch at the pushed head and puts the
// destination workspace on it, files untouched: a worktree added
// --no-checkout when absent, HEAD switched when it is ours (a move back,
// or main mode). A diverged branch is refused before any ref changes.
func GitPrepare(ctx context.Context, m Manifest, locks *KeyedLocks) (GitPrepareResult, error) {
	if !m.gitMode() {
		return GitPrepareResult{}, errors.New("git-prepare needs a main or linked git manifest")
	}
	canonical, ws, name := gitTarget(m)
	defer locks.Lock(canonical)()
	newSHA, err := git.RefSHA(ctx, canonical, MoveRef(name, "head"))
	if err != nil {
		return GitPrepareResult{}, err
	}
	if newSHA == "" {
		return GitPrepareResult{}, fmt.Errorf("%s has no %s: the push did not arrive", canonical, MoveRef(name, "head"))
	}
	list, err := git.WorktreeList(ctx, canonical)
	if err != nil {
		return GitPrepareResult{}, err
	}
	branch := m.TargetBranch()
	ref := ""
	if branch != "" {
		ref = "refs/heads/" + branch
		if err := prepareBranch(ctx, canonical, ws, branch, newSHA, m.IsCopy(), list); err != nil {
			return GitPrepareResult{}, err
		}
	}
	mainMode := m.Git.Mode == GitModeMain && !m.IsCopy()
	if mainMode || registered(list, ws) {
		err = git.SetHEAD(ctx, ws, ref, newSHA)
	} else {
		err = git.WorktreeAddNoCheckout(ctx, canonical, ws, branch, newSHA)
	}
	if err != nil {
		return GitPrepareResult{}, err
	}
	return destModules(ctx, ws)
}

func registered(list []git.Worktree, ws string) bool {
	for _, w := range list {
		if git.SamePath(w.Path, ws) {
			return true
		}
	}
	return false
}

// prepareBranch fast-forwards (or creates) branch to newSHA with a
// compare-and-swap, refusing a branch held by another worktree or one with
// commits the source lacks. A copy's branch must be new, or its own partial's.
func prepareBranch(ctx context.Context, canonical, ws, branch, newSHA string, isCopy bool, list []git.Worktree) error {
	at, held := git.CheckedOut(list)[branch]
	ours := held && git.SamePath(at, ws)
	ref := "refs/heads/" + branch
	old, err := git.RefSHA(ctx, canonical, ref)
	if err != nil {
		return err
	}
	switch {
	case isCopy && (old != "" || held) && !ours:
		return problemf(CodeBranchExists, "branch %s already exists in %s", branch, canonical)
	case held && !ours:
		return problemf(CodeBranchCheckedOut, "branch %s is checked out at %s", branch, at)
	}
	if old == newSHA {
		return nil
	}
	if old != "" {
		ok, err := git.IsAncestor(ctx, canonical, old, newSHA)
		if err != nil {
			return err
		}
		if !ok {
			return problemf(CodeBranchDiverged, "branch %s in %s has commits the source lacks (%s); merge or rebase there first", branch, canonical, old)
		}
	}
	return git.UpdateRefCAS(ctx, canonical, ref, newSHA, old)
}

// destModules checks ws's modules dir is private to it and reports it.
func destModules(ctx context.Context, ws string) (GitPrepareResult, error) {
	modules, err := git.ModulesDir(ctx, ws)
	if err != nil {
		return GitPrepareResult{}, err
	}
	gitDir, err := git.AbsoluteGitDir(ctx, ws)
	if err != nil {
		return GitPrepareResult{}, err
	}
	common, err := git.CommonDir(ctx, ws)
	if err != nil {
		return GitPrepareResult{}, err
	}
	mode := GitModeLinked
	if git.SamePath(gitDir, common) {
		mode = GitModeMain
	}
	if err := checkModulesDir(mode, modules, common, gitDir); err != nil {
		return GitPrepareResult{}, err
	}
	return GitPrepareResult{ModulesDir: modules, GitDir: gitDir}, nil
}

// GitFinalize (G4) runs after the workspace and modules rsyncs: a copy's
// partial worktree and modules are renamed into place, the index is
// restored from the pushed tree, absorbed submodules are relinked and
// verified (a copy's attached ones forked to <b>-<new>), shared submodule
// config is set where unset, and the temp refs are deleted. It returns the
// non-fatal notes. Every step is idempotent, so a re-run converges.
func GitFinalize(ctx context.Context, m Manifest, locks *KeyedLocks) ([]Problem, error) {
	canonical, ws, name := gitTarget(m)
	defer locks.Lock(canonical)()
	if m.IsCopy() {
		var err error
		if ws, err = placeCopy(ctx, canonical, ws, m.TargetWorkspace()); err != nil {
			return nil, err
		}
	}
	if err := git.ReadTree(ctx, ws, MoveRef(name, "index")+"^{tree}"); err != nil {
		return nil, err
	}
	if err := relinkSubmodules(ctx, ws, m); err != nil {
		return nil, err
	}
	notes, err := shareSubmoduleConfig(ctx, ws, m.Git.Submodules)
	if err != nil {
		return nil, err
	}
	if err := git.RefreshIndex(ctx, ws); err != nil {
		return nil, err
	}
	if !m.IsCopy() {
		if err := setUpstream(ctx, ws, m.TargetBranch(), m.Git.Upstream); err != nil {
			return nil, err
		}
	}
	return notes, git.DeleteRefs(ctx, canonical, MoveRef(name, "head"), MoveRef(name, "index"))
}

// placeCopy moves a copy's partial worktree to its final path (once), then
// its staged modules into the worktree's git dir.
func placeCopy(ctx context.Context, canonical, partial, final string) (string, error) {
	if _, err := os.Stat(partial); err == nil {
		if err := git.WorktreeMove(ctx, canonical, partial, final); err != nil {
			return "", err
		}
	}
	gitDir, err := git.AbsoluteGitDir(ctx, final)
	if err != nil {
		return "", err
	}
	staged := filepath.Join(gitDir, ModulesPartial)
	if _, err := os.Stat(staged); errors.Is(err, fs.ErrNotExist) {
		return final, nil
	}
	if err := os.Rename(staged, filepath.Join(gitDir, "modules")); err != nil {
		return "", fmt.Errorf("placing the copy's submodules: %w", err)
	}
	return final, nil
}

// relinkSubmodules repoints each absorbed submodule (children first) at its
// gitdir under this workspace's modules dir and verifies it.
func relinkSubmodules(ctx context.Context, ws string, m Manifest) error {
	modules, err := git.ModulesDir(ctx, ws)
	if err != nil {
		return err
	}
	for _, s := range m.Git.Submodules {
		if s.State != git.SubmoduleAbsorbed {
			continue
		}
		if err := validateSubmoduleToken(s.Path); err != nil {
			return err
		}
		gitDir := filepath.Join(modules, s.GitDir)
		if _, ok := under(filepath.Clean(modules), gitDir); !ok {
			return problemf(CodeUnsafePath, "submodule %s gitdir %q leaves %s", s.Path, s.GitDir, modules)
		}
		if err := relinkOne(ctx, ws, s, gitDir, m); err != nil {
			return problemf(CodeSubmoduleTorn, "submodule %s: %v; re-run the %s", s.Path, err, m.Mode)
		}
	}
	return nil
}

func relinkOne(ctx context.Context, ws string, s git.Submodule, gitDir string, m Manifest) error {
	if err := git.RelinkSubmodule(ctx, ws, s.Path, gitDir); err != nil {
		return err
	}
	dir := filepath.Join(ws, s.Path)
	if err := git.VerifyHead(ctx, dir); err != nil {
		return err
	}
	if !m.IsCopy() {
		return nil
	}
	if err := git.FsckConnectivity(ctx, dir); err != nil {
		return err
	}
	return forkSubmodule(ctx, dir, s, m.Copy.Name)
}

// forkSubmodule puts a copy's submodule on <b>-<new> when the source's was
// on <b>; a detached one stays detached. A re-run finds it already forked.
func forkSubmodule(ctx context.Context, dir string, s git.Submodule, newName string) error {
	b := strings.TrimPrefix(s.HeadRef, "refs/heads/")
	if b == "" {
		return nil
	}
	fork := b + "-" + newName
	if head, err := git.HeadState(ctx, dir); err == nil && head.Ref == "refs/heads/"+fork {
		return nil
	}
	return git.ForkSubmoduleBranch(ctx, dir, fork, s.HeadSHA)
}

// shareSubmoduleConfig sets each submodule's url and active in the shared
// config only where unset: sibling worktrees keep the value they use.
func shareSubmoduleConfig(ctx context.Context, ws string, subs []git.Submodule) ([]Problem, error) {
	var notes []Problem
	for _, s := range subs {
		if s.Name == "" {
			continue
		}
		for _, kv := range [][2]string{{"url", s.URL}, {"active", s.Active}} {
			if kv[1] == "" {
				continue
			}
			key := "submodule." + s.Name + "." + kv[0]
			set, err := git.SetConfigIfUnset(ctx, ws, key, kv[1])
			if err != nil {
				return nil, err
			}
			if have := git.ConfigGet(ctx, ws, key); !set && kv[0] == "url" && have != kv[1] {
				notes = append(notes, Problem{Code: NoteSubmoduleURLDiffers, Message: fmt.Sprintf("submodule %s keeps this host's url %s (the source uses %s)", s.Path, have, kv[1])})
			}
		}
		if p := localURLPath(s.URL); p != "" {
			if _, err := os.Stat(p); err != nil {
				notes = append(notes, Problem{Code: NoteSubmoduleURLLocal, Message: fmt.Sprintf("submodule %s's url %s is a local path missing on this host; fetches there will fail", s.Path, s.URL)})
			}
		}
	}
	return notes, nil
}

func localURLPath(url string) string {
	switch {
	case strings.HasPrefix(url, "file://"):
		return strings.TrimPrefix(url, "file://")
	case strings.HasPrefix(url, "/"):
		return url
	}
	return ""
}

// setUpstream sets branch's tracking config only where unset and only when
// its remote exists here.
func setUpstream(ctx context.Context, ws, branch string, up *Upstream) error {
	if branch == "" || up == nil || up.Remote == "" {
		return nil
	}
	if up.Remote != "." {
		if _, err := git.ExecGit(ctx, ws, "remote", "get-url", up.Remote); err != nil {
			return nil
		}
	}
	for _, kv := range [][2]string{{"remote", up.Remote}, {"merge", up.Merge}} {
		if _, err := git.SetConfigIfUnset(ctx, ws, "branch."+branch+"."+kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// ForkPlainBranch puts a plain-mode copy's own repository on branch at its
// HEAD, and each attached absorbed submodule on <b>-<newName>; index and
// files are untouched. Outside git, or with no branch, it does nothing.
func ForkPlainBranch(ctx context.Context, ws, branch, newName string) error {
	if branch == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	head, err := git.HeadState(ctx, ws)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	if head.Ref != ref {
		if err := git.UpdateRefCAS(ctx, ws, ref, head.SHA, ""); err != nil {
			return problemf(CodeBranchExists, "branch %s already exists in the copy: %v", branch, err)
		}
		if err := git.SetHEAD(ctx, ws, ref, ""); err != nil {
			return err
		}
	}
	subs, err := git.Submodules(ctx, ws)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.State == git.SubmoduleAbsorbed {
			if err := forkSubmodule(ctx, filepath.Join(ws, s.Path), s, newName); err != nil {
				return fmt.Errorf("submodule %s: %w", s.Path, err)
			}
		}
	}
	return nil
}
