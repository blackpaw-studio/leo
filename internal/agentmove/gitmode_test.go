package agentmove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
)

func TestGitModeLinkedMainPlain(t *testing.T) {
	ctx := context.Background()
	plain := gittest.NewRepo(t)
	info, err := DetectGit(ctx, plain, "")
	if err != nil || !info.IsRepo || info.Mode != GitModePlain {
		t.Fatalf("plain repo = %+v, %v", info, err)
	}

	canon := gittest.NewRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "feat", wt)
	main, err := DetectGit(ctx, canon, "")
	if err != nil || main.Mode != GitModeMain || main.Canonical != canon {
		t.Fatalf("canonical with a worktree = %+v, %v", main, err)
	}
	linked, err := DetectGit(ctx, wt, canon)
	if err != nil || linked.Mode != GitModeLinked || linked.Canonical != canon {
		t.Fatalf("linked worktree = %+v, %v", linked, err)
	}
	if linked.HeadRef != "refs/heads/feat" || linked.HeadSHA == "" || len(linked.RootSHAs) != 1 {
		t.Fatalf("linked head/roots = %+v", linked)
	}

	notRepo, err := DetectGit(ctx, t.TempDir(), "")
	if err != nil || notRepo.IsRepo || notRepo.Mode != GitModePlain {
		t.Fatalf("plain dir = %+v, %v", notRepo, err)
	}
}

func TestGitModeCanonicalMustMatchRecord(t *testing.T) {
	canon := gittest.NewRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "feat", wt)
	_, err := DetectGit(context.Background(), wt, "/some/other/canonical")
	if !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "/some/other/canonical") {
		t.Fatalf("DetectGit = %v, want git_unsupported naming the record's canonical", err)
	}
}

// gitModeRepo returns a canonical in git mode (it has a linked worktree).
func gitModeRepo(t *testing.T) string {
	t.Helper()
	canon := gittest.NewRepo(t)
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "side", filepath.Join(t.TempDir(), "side"))
	return canon
}

func checkGit(t *testing.T, ws string) error {
	t.Helper()
	ctx := context.Background()
	info, err := DetectGit(ctx, ws, "")
	if err != nil {
		return err
	}
	return CheckGitMode(ctx, ws, info)
}

func TestGitModeRefusesShallowSparse(t *testing.T) {
	src := gittest.NewRepo(t)
	gittest.Write(t, filepath.Join(src, "a.txt"), "two\n")
	gittest.Run(t, src, "commit", "-qam", "two")
	shallow := filepath.Join(t.TempDir(), "shallow")
	gittest.Run(t, src, "clone", "-q", "--depth", "1", "file://"+src, shallow)
	gittest.Run(t, shallow, "worktree", "add", "-q", "-b", "side", filepath.Join(t.TempDir(), "s"))
	if err := checkGit(t, shallow); !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("shallow = %v, want git_unsupported", err)
	}

	sparse := gitModeRepo(t)
	gittest.Run(t, sparse, "config", "core.sparseCheckout", "true")
	if err := checkGit(t, sparse); !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "sparse") {
		t.Fatalf("sparse = %v, want git_unsupported", err)
	}

	merging := gitModeRepo(t)
	head := gittest.Run(t, merging, "rev-parse", "HEAD")
	gittest.Write(t, filepath.Join(merging, ".git", "MERGE_HEAD"), head+"\n")
	if err := checkGit(t, merging); !HasCode(err, CodeGitOpInProgress) {
		t.Fatalf("mid-merge = %v, want git_op_in_progress", err)
	}

	if err := checkGit(t, gitModeRepo(t)); err != nil {
		t.Fatalf("a clean git-mode repo = %v", err)
	}
}

func TestGitModeEmptyGitmodulesNotRefused(t *testing.T) {
	canon := gitModeRepo(t)
	gittest.Write(t, filepath.Join(canon, ".gitmodules"), "")
	gittest.Run(t, canon, "add", ".gitmodules")
	gittest.Run(t, canon, "commit", "-qm", "empty gitmodules")
	if err := checkGit(t, canon); err != nil {
		t.Fatalf("empty .gitmodules = %v, want no refusal", err)
	}
}

func TestGitModeRefusesSharedModulesDir(t *testing.T) {
	if err := checkModulesDir(GitModeLinked, "/c/.git/modules", "/c/.git", "/c/.git/worktrees/wt"); !HasCode(err, CodeGitUnsupported) {
		t.Fatalf("shared modules dir = %v, want git_unsupported", err)
	}
	if err := checkModulesDir(GitModeLinked, "/c/.git/worktrees/wt/modules", "/c/.git", "/c/.git/worktrees/wt"); err != nil {
		t.Fatalf("private modules dir = %v", err)
	}
	if err := checkModulesDir(GitModeMain, "/c/.git/modules", "/c/.git", "/c/.git"); err != nil {
		t.Fatalf("main modules dir = %v", err)
	}
}

func TestGitModeRefusesSubmoduleWorktreesAndAlternates(t *testing.T) {
	ctx := context.Background()
	super := gittest.SuperWithSubmodules(t)
	gittest.Run(t, super, "worktree", "add", "-q", "--no-checkout", "-b", "side", filepath.Join(t.TempDir(), "side"))

	info, err := DetectGit(ctx, super, "")
	if err != nil || len(info.Submodules) != 5 || info.ModulesDir == "" {
		t.Fatalf("DetectGit = %+v, %v; want 5 submodules and a modules dir", info, err)
	}
	if err := CheckGitMode(ctx, super, info); err != nil {
		t.Fatalf("submodules in git mode = %v, want accepted", err)
	}
	alt := filepath.Join(t.TempDir(), "objects")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Write(t, filepath.Join(info.ModulesDir, "libs/a/objects/info/alternates"), alt+"\n")
	if err := checkGit(t, super); !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "alternates") {
		t.Fatalf("submodule with alternates = %v, want git_unsupported", err)
	}

	// The remaining rules are filesystem checks on the scanned gitdir.
	modules := t.TempDir()
	gitDir := filepath.Join(modules, "libs/b")
	gittest.Write(t, filepath.Join(gitDir, "worktrees/b-wt/HEAD"), "x\n")
	sub := git.Submodule{Path: "libs/b", Name: "b", State: git.SubmoduleAbsorbed, GitDir: gitDir}
	if err := checkSubmodule(sub, modules); !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("submodule with its own worktree = %v, want git_unsupported", err)
	}
	sub.GitDir = t.TempDir()
	if err := checkSubmodule(sub, modules); !HasCode(err, CodeGitUnsupported) || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("gitdir outside the modules dir = %v, want git_unsupported", err)
	}
	inline := git.Submodule{Path: "libs/c", State: git.SubmoduleInline}
	if err := checkSubmodule(inline, modules); err != nil {
		t.Fatalf("inline submodule = %v: only absorbed gitdirs are checked", err)
	}
}

func TestSubmodulePathNameValidation(t *testing.T) {
	for _, good := range []string{"libs/a", "deep", "a.b-c_d@e%f+g=h,i"} {
		if err := validateSubmoduleToken(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"has space", "a/../b", "..", "-lead", "a*b", "a[b", "a$b", "a\nb", "/abs"} {
		if err := validateSubmoduleToken(bad); !HasCode(err, CodeUnsafePath) {
			t.Errorf("%q = %v, want unsafe_path", bad, err)
		}
	}
}

func TestGitModeNestedForeignWorktreesSkipped(t *testing.T) {
	canon := gittest.NewRepo(t)
	nested := filepath.Join(canon, ".claude", "worktrees", "agent-1")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "agent-1", nested)
	info, err := DetectGit(context.Background(), canon, "")
	if err != nil || info.Mode != GitModeMain {
		t.Fatalf("DetectGit = %+v, %v", info, err)
	}
	if len(info.NestedWorktrees) != 1 || info.NestedWorktrees[0] != ".claude/worktrees/agent-1" {
		t.Fatalf("NestedWorktrees = %v", info.NestedWorktrees)
	}
	if notes := info.Notes(); len(notes) != 1 || notes[0].Code != NoteNestedWorktreeSkip || !strings.Contains(notes[0].Message, ".claude/worktrees/agent-1") {
		t.Fatalf("notes = %+v", notes)
	}
	spec := info.WorkspaceSpec(canon, nil, nil)
	ex := strings.Join(spec.Excludes(), " ")
	if !strings.Contains(ex, "/.git") || !strings.Contains(ex, "/.claude/worktrees/agent-1/") {
		t.Fatalf("excludes = %s", ex)
	}
}

func TestWorkspaceRsyncExcludesDotGitInGitMode(t *testing.T) {
	ctx := context.Background()
	canon := gittest.NewRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "feat", wt)
	linked, err := DetectGit(ctx, wt, "")
	if err != nil {
		t.Fatal(err)
	}
	if ex := linked.WorkspaceSpec(wt, []string{"node_modules/"}, nil).Excludes(); !containsStr(ex, "/.git") || ex[0] != "node_modules/" {
		t.Fatalf("linked excludes = %v, want the user's rules then /.git", ex)
	}
	plain, err := DetectGit(ctx, gittest.NewRepo(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if ex := plain.WorkspaceSpec("/ws", nil, nil).Excludes(); containsStr(ex, "/.git") {
		t.Fatalf("plain excludes = %v: plain mode sends .git", ex)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
