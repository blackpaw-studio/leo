package agentmove

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
)

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	gittest.Write(t, filepath.Join(dir, name), content)
	gittest.Run(t, dir, "add", name)
	gittest.Run(t, dir, "commit", "-qm", name)
}

// srcWorktree is a canonical with a linked worktree on feat one commit
// ahead of main.
func srcWorktree(t *testing.T) (canon, wt string) {
	t.Helper()
	canon = gittest.NewRepo(t)
	wt = filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "feat", wt)
	commitFile(t, wt, "f.txt", "f\n")
	return canon, wt
}

// cloneOf is a destination canonical sharing src's history, with no origin.
func cloneOf(t *testing.T, src string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "dest")
	gittest.Run(t, filepath.Dir(dest), "clone", "-q", src, dest)
	gittest.Run(t, dest, "remote", "remove", "origin")
	return dest
}

// pushMove runs G1 and G2 on one host: snapshot ws's index and push HEAD
// and the snapshot into dest's refs for name.
func pushMove(t *testing.T, ws, dest, name string) {
	t.Helper()
	if _, err := git.SnapshotIndex(context.Background(), ws, MoveRef(name, "index")); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "push", "-q", "-f", dest, "HEAD:"+MoveRef(name, "head"), MoveRef(name, "index")+":"+MoveRef(name, "index"))
}

// moveOf is the manifest moving ws (as detected) to destWS in destCanon.
func moveOf(t *testing.T, ws, destCanon, destWS, name string) (Manifest, GitInfo) {
	t.Helper()
	ctx := context.Background()
	info, err := DetectGit(ctx, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	gm, err := info.Manifest(ctx, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	gm.Canonical = destCanon
	return Manifest{Protocol: Protocol, Mode: ModeMove, Agent: agentstore.Record{Name: name, Workspace: destWS}, Git: gm}, info
}

func rsync(args []string) error {
	if out, err := exec.Command("rsync", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("rsync %v: %w\n%s", args, err, out)
	}
	return nil
}

// land runs the destination and transfer stages after the push: G3, the
// workspace and modules rsyncs, G4. It never calls t.Fatal, so goroutines
// may run it.
func land(srcWS string, m Manifest, info GitInfo, locks *KeyedLocks) ([]Problem, error) {
	ctx := context.Background()
	res, err := GitPrepare(ctx, m, locks)
	if err != nil {
		return nil, fmt.Errorf("G3: %w", err)
	}
	_, destWS, _ := gitTarget(m)
	if err := rsync(WorkspaceRsyncArgv("", srcWS, destWS, info.WorkspaceSpec(srcWS, nil, nil).Excludes())); err != nil {
		return nil, err
	}
	if _, err := os.Stat(info.ModulesDir); err == nil {
		if err := rsync(ModulesRsyncArgv("", info.ModulesDir, ModulesTarget(m.IsCopy(), res.ModulesDir, res.GitDir))); err != nil {
			return nil, err
		}
	}
	notes, err := GitFinalize(ctx, m, locks)
	if err != nil {
		return nil, fmt.Errorf("G4: %w", err)
	}
	return notes, nil
}

// snapshot is everything a move must preserve in dir and its submodules.
func snapshot(t *testing.T, dir string, subs ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(gittest.Status(t, dir) + "\n" + gittest.Run(t, dir, "diff", "--cached") + "\n")
	for _, s := range subs {
		d := filepath.Join(dir, s)
		fmt.Fprintf(&b, "== %s\n%s\n%s\n%s\n%s\n", s, gittest.Status(t, d), gittest.Run(t, d, "diff", "--cached"),
			gittest.Run(t, d, "stash", "list"), gittest.Run(t, d, "for-each-ref", "--format=%(refname) %(objectname)"))
	}
	// Top-level paths only: the fixture's embedded repo has no .gitmodules
	// mapping, which `submodule status` dies on.
	var top []string
	for _, s := range subs {
		if !slices.ContainsFunc(subs, func(p string) bool { return strings.HasPrefix(s, p+"/") }) {
			top = append(top, s)
		}
	}
	if len(top) > 0 {
		b.WriteString(gittest.Run(t, dir, append([]string{"submodule", "status", "--recursive", "--"}, top...)...))
	}
	return b.String()
}

// treeHash hashes every file under dir by path and content.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%x\x00", rel, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestGitPrepareCreatesWorktreeNoCheckout(t *testing.T) {
	ctx := context.Background()
	canon, wt := srcWorktree(t)
	dest := cloneOf(t, canon)
	destWS := filepath.Join(t.TempDir(), "ws")
	m, _ := moveOf(t, wt, dest, destWS, "web")
	pushMove(t, wt, dest, "web")

	res, err := GitPrepare(ctx, m, &KeyedLocks{})
	if err != nil {
		t.Fatalf("GitPrepare: %v", err)
	}
	if got := gittest.Run(t, destWS, "symbolic-ref", "HEAD"); got != "refs/heads/feat" {
		t.Errorf("HEAD = %s, want refs/heads/feat", got)
	}
	if got, want := gittest.Run(t, destWS, "rev-parse", "HEAD"), gittest.Run(t, wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("feat = %s, want the pushed %s", got, want)
	}
	entries, _ := os.ReadDir(destWS)
	if len(entries) != 1 || entries[0].Name() != ".git" {
		t.Errorf("workspace holds %v: G3 must not check files out", entries)
	}
	if !strings.Contains(res.GitDir, filepath.Join(".git", "worktrees")) || !git.SamePath(res.ModulesDir, filepath.Join(res.GitDir, "modules")) {
		t.Errorf("result = %+v, want the worktree's private modules dir", res)
	}
}

func TestGitPrepareMoveBackFastForwardsOwnWorktree(t *testing.T) {
	ctx := context.Background()
	canon, wt := srcWorktree(t)
	dest := cloneOf(t, canon)
	destWS := filepath.Join(t.TempDir(), "ws")
	m, _ := moveOf(t, wt, dest, destWS, "web")
	pushMove(t, wt, dest, "web")
	if _, err := GitPrepare(ctx, m, &KeyedLocks{}); err != nil {
		t.Fatal(err)
	}
	before := len(strings.Split(gittest.Run(t, dest, "worktree", "list"), "\n"))

	commitFile(t, wt, "g.txt", "g\n") // the agent worked elsewhere, then comes back
	pushMove(t, wt, dest, "web")
	if _, err := GitPrepare(ctx, m, &KeyedLocks{}); err != nil {
		t.Fatalf("move back: %v", err)
	}
	if got, want := gittest.Run(t, dest, "rev-parse", "feat"), gittest.Run(t, wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("feat = %s, want fast-forwarded to %s", got, want)
	}
	if got := gittest.Run(t, destWS, "symbolic-ref", "HEAD"); got != "refs/heads/feat" {
		t.Errorf("HEAD = %s", got)
	}
	if after := len(strings.Split(gittest.Run(t, dest, "worktree", "list"), "\n")); after != before {
		t.Errorf("worktrees %d → %d: the move back must reuse its own worktree", before, after)
	}
}

func TestGitPrepareRefusesDivergedBranch(t *testing.T) {
	canon, wt := srcWorktree(t)
	dest := cloneOf(t, canon)
	gittest.Run(t, dest, "branch", "feat", "main")
	commitFile(t, dest, "local.txt", "dest only\n")
	gittest.Run(t, dest, "branch", "-f", "feat", "HEAD")
	destFeat := gittest.Run(t, dest, "rev-parse", "feat")
	destWS := filepath.Join(t.TempDir(), "ws")
	m, _ := moveOf(t, wt, dest, destWS, "web")
	pushMove(t, wt, dest, "web")

	_, err := GitPrepare(context.Background(), m, &KeyedLocks{})
	if !HasCode(err, CodeBranchDiverged) {
		t.Fatalf("GitPrepare = %v, want branch_diverged", err)
	}
	if got := gittest.Run(t, dest, "rev-parse", "feat"); got != destFeat {
		t.Errorf("feat moved to %s on a refusal", got)
	}
	if _, err := os.Stat(destWS); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("workspace created on a refusal: %v", err)
	}
}

func TestGitPrepareRefusesBranchCheckedOutElsewhere(t *testing.T) {
	canon, wt := srcWorktree(t)
	dest := cloneOf(t, canon)
	other := filepath.Join(t.TempDir(), "other")
	gittest.Run(t, dest, "worktree", "add", "-q", "-b", "feat", other, "main")
	m, _ := moveOf(t, wt, dest, filepath.Join(t.TempDir(), "ws"), "web")
	pushMove(t, wt, dest, "web")
	_, err := GitPrepare(context.Background(), m, &KeyedLocks{})
	if !HasCode(err, CodeBranchCheckedOut) || !strings.Contains(err.Error(), "other") {
		t.Fatalf("GitPrepare = %v, want branch_checked_out naming the other worktree", err)
	}
}

func TestGitPrepareRefusesSharedModulesDir(t *testing.T) {
	canon, wt := srcWorktree(t)
	dest := cloneOf(t, canon)
	m, _ := moveOf(t, wt, dest, filepath.Join(t.TempDir(), "ws"), "web")
	pushMove(t, wt, dest, "web")
	real := git.ExecGit
	t.Cleanup(func() { git.ExecGit = real })
	// An older git that shares modules across worktrees.
	git.ExecGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if slices.Contains(args, "--git-path") && args[len(args)-1] == "modules" {
			return []byte(filepath.Join(dest, ".git", "modules") + "\n"), nil
		}
		return real(ctx, dir, args...)
	}
	if _, err := GitPrepare(context.Background(), m, &KeyedLocks{}); !HasCode(err, CodeGitUnsupported) {
		t.Fatalf("GitPrepare = %v, want git_unsupported", err)
	}
}

func TestSeedClonesViaPartialRename(t *testing.T) {
	canonical := filepath.Join(t.TempDir(), "src", "app")
	partial := canonical + PartialSuffix
	gittest.Write(t, filepath.Join(partial, "stale"), "from a failed run\n")
	r := &fakeRunner{answer: func(c Cmd) ([]byte, error) {
		if _, err := os.Stat(filepath.Join(partial, "stale")); err == nil {
			t.Error("the failed run's partial was not cleared before cloning")
		}
		gittest.Write(t, filepath.Join(c.Args[3], ".git", "HEAD"), "ref: refs/heads/main\n")
		return nil, nil
	}}
	if err := SeedCanonical(context.Background(), r, "me/app", canonical); err != nil {
		t.Fatalf("SeedCanonical: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].Name != "gh" || !slices.Equal(r.calls[0].Args, []string{"repo", "clone", "me/app", partial}) {
		t.Fatalf("calls = %+v", r.calls)
	}
	if _, err := os.Stat(filepath.Join(canonical, ".git", "HEAD")); err != nil {
		t.Errorf("clone not renamed into place: %v", err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("partial left behind: %v", err)
	}
	if err := SeedCanonical(context.Background(), r, "me/app", canonical); err != nil || len(r.calls) != 1 {
		t.Errorf("an existing canonical was cloned again (%v, %d calls)", err, len(r.calls))
	}
	if err := SeedCanonical(context.Background(), r, "--upload-pack=x", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("a non owner/repo argument reached gh")
	}
}

// superWorktree is a linked worktree of the submodule fixture with libs/a
// (and its nested deep) and libs/inl initialised in its private modules.
func superWorktree(t *testing.T) (super, ws string) {
	t.Helper()
	super = gittest.SuperWithSubmodules(t)
	ws = filepath.Join(t.TempDir(), "src", "ws")
	gittest.Run(t, super, "worktree", "add", "-q", "-b", "feat", ws)
	gittest.Run(t, ws, "submodule", "update", "-q", "--init", "--recursive", "--", "libs/a", "libs/inl")
	return super, ws
}

func TestReceiveSubmodulesRoundTripPreservesStatus(t *testing.T) {
	ctx := context.Background()
	super, ws := superWorktree(t)
	// Superproject: a commit, a staged and an unstaged edit, an untracked file.
	commitFile(t, ws, "s.txt", "s\n")
	gittest.Write(t, filepath.Join(ws, "top.txt"), "staged\n")
	gittest.Run(t, ws, "add", "top.txt")
	gittest.Write(t, filepath.Join(ws, "top.txt"), "unstaged\n")
	gittest.Write(t, filepath.Join(ws, "untracked.txt"), "u\n")
	// libs/a: a local branch with an unpushed commit, a stash, staged and
	// unstaged edits; deep: a dirty file.
	a := filepath.Join(ws, "libs/a")
	gittest.Run(t, a, "checkout", "-q", "-b", "work")
	commitFile(t, a, "w.txt", "w\n")
	gittest.Write(t, filepath.Join(a, "x.txt"), "stashed\n")
	gittest.Run(t, a, "stash", "-q")
	gittest.Write(t, filepath.Join(a, "x.txt"), "staged\n")
	gittest.Run(t, a, "add", "x.txt")
	gittest.Write(t, filepath.Join(a, "x.txt"), "unstaged\n")
	gittest.Write(t, filepath.Join(a, "deep", "deep.txt"), "dirty\n")
	subs := []string{"libs/a", "libs/a/deep", "libs/inl"}
	want := snapshot(t, ws, subs...)
	srcURL := gittest.Run(t, super, "config", "--get", "submodule.libs/a.url")

	// The destination canonical has no origin, and already holds a worktree
	// named ws, so this one gets a different admin id.
	dest := cloneOf(t, super)
	gittest.Run(t, dest, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "taken", "ws"))
	destWS := filepath.Join(t.TempDir(), "dst", "ws")
	m, info := moveOf(t, ws, dest, destWS, "web")
	pushMove(t, ws, dest, "web")
	notes, err := land(ws, m, info, &KeyedLocks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v", notes)
	}
	if got := snapshot(t, destWS, subs...); got != want {
		t.Fatalf("destination state differs.\nsource:\n%s\ndestination:\n%s", want, got)
	}
	srcID := filepath.Base(gittest.Run(t, ws, "rev-parse", "--absolute-git-dir"))
	if dstID := filepath.Base(gittest.Run(t, destWS, "rev-parse", "--absolute-git-dir")); dstID == srcID {
		t.Errorf("worktree ids match (%s): the relink went untested", dstID)
	}
	if got := gittest.Run(t, dest, "config", "--get", "submodule.libs/a.url"); got != srcURL {
		t.Errorf("submodule url = %s, want the source's resolved %s (never re-resolved)", got, srcURL)
	}
	if refs := gittest.Run(t, dest, "for-each-ref", "refs/leo-move/"); refs != "" {
		t.Errorf("temp refs left behind: %s", refs)
	}
	if _, err := GitFinalize(ctx, m, &KeyedLocks{}); err == nil {
		t.Error("G4 re-ran without its refs: want an error, not a silent no-op")
	}
}

func TestReceiveSubmoduleConfigKeepsDestValue(t *testing.T) {
	ctx := context.Background()
	repo := gittest.NewRepo(t)
	gittest.Run(t, repo, "config", "submodule.libs/a.url", "https://dest.example/a.git")
	missing := filepath.Join(t.TempDir(), "gone.git")
	subs := []git.Submodule{
		{Path: "libs/a", Name: "libs/a", URL: "https://src.example/a.git", Active: "true"},
		{Path: "libs/b", Name: "libs/b", URL: missing},
	}
	notes, err := shareSubmoduleConfig(ctx, repo, subs)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, repo, "config", "--get", "submodule.libs/a.url"); got != "https://dest.example/a.git" {
		t.Errorf("dest url overwritten: %s", got)
	}
	if got := gittest.Run(t, repo, "config", "--get", "submodule.libs/a.active"); got != "true" {
		t.Errorf("unset active not copied: %q", got)
	}
	if got := gittest.Run(t, repo, "config", "--get", "submodule.libs/b.url"); got != missing {
		t.Errorf("unset url not copied: %q", got)
	}
	if codes(notes) != NoteSubmoduleURLDiffers+","+NoteSubmoduleURLLocal {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestMainModeLeavesDestLinkedWorktrees(t *testing.T) {
	src := gittest.NewRepo(t)
	gittest.Run(t, src, "worktree", "add", "-q", "-b", "side", filepath.Join(t.TempDir(), "side"))
	dest := cloneOf(t, src)
	sib := filepath.Join(t.TempDir(), "sib")
	gittest.Run(t, dest, "worktree", "add", "-q", "-b", "sib", sib)
	gittest.Write(t, filepath.Join(sib, "a.txt"), "sibling's own edit\n")
	sibBefore := snapshot(t, sib)

	commitFile(t, src, "n.txt", "n\n")
	gittest.Write(t, filepath.Join(src, "a.txt"), "staged\n")
	gittest.Run(t, src, "add", "a.txt")
	gittest.Write(t, filepath.Join(src, "u.txt"), "untracked\n")
	want := snapshot(t, src)

	m, info := moveOf(t, src, dest, dest, "web")
	if m.Git.Mode != GitModeMain {
		t.Fatalf("mode = %s", m.Git.Mode)
	}
	pushMove(t, src, dest, "web")
	if _, err := land(src, m, info, &KeyedLocks{}); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, dest); got != want {
		t.Fatalf("canonical differs.\nsource:\n%s\ndestination:\n%s", want, got)
	}
	if got := snapshot(t, sib); got != sibBefore {
		t.Fatalf("sibling worktree changed:\n%s\nwas\n%s", got, sibBefore)
	}
	if !strings.Contains(gittest.Run(t, dest, "worktree", "list"), sib) && !strings.Contains(gittest.Run(t, dest, "worktree", "list"), filepath.Base(sib)) {
		t.Fatal("sibling worktree unregistered")
	}
}

func TestConcurrentReceivesShareCanonical(t *testing.T) {
	super, ws1 := superWorktree(t)
	gittest.Write(t, filepath.Join(ws1, "libs/a/x.txt"), "dirty submodule\n")
	commitFile(t, ws1, "one.txt", "1\n")
	ws2 := filepath.Join(t.TempDir(), "src2", "ws2")
	gittest.Run(t, super, "worktree", "add", "-q", "-b", "two", ws2)
	gittest.Write(t, filepath.Join(ws2, "top.txt"), "two's edit\n")

	dest := cloneOf(t, super)
	gittest.Run(t, dest, "config", "submodule.libs/a.url", gittest.Run(t, super, "config", "--get", "submodule.libs/a.url"))
	sib := filepath.Join(t.TempDir(), "sib")
	gittest.Run(t, dest, "worktree", "add", "-q", "-b", "sib", sib)
	gittest.Run(t, sib, "submodule", "update", "-q", "--init", "--", "libs/a")
	gittest.Write(t, filepath.Join(sib, "libs/a/x.txt"), "sibling's own\n")
	sibBefore, sibModules := snapshot(t, sib, "libs/a"), gittest.Run(t, sib, "rev-parse", "--path-format=absolute", "--git-path", "modules")
	sibTree := treeHash(t, sibModules)

	want1, want2 := snapshot(t, ws1, "libs/a", "libs/a/deep"), snapshot(t, ws2)
	m1, info1 := moveOf(t, ws1, dest, filepath.Join(t.TempDir(), "d1", "ws1"), "one")
	m2, info2 := moveOf(t, ws2, dest, filepath.Join(t.TempDir(), "d2", "ws2"), "two")
	pushMove(t, ws1, dest, "one")
	pushMove(t, ws2, dest, "two")

	locks := &KeyedLocks{}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, job := range []struct {
		src  string
		m    Manifest
		info GitInfo
	}{{ws1, m1, info1}, {ws2, m2, info2}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = land(job.src, job.m, job.info, locks)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, m1.Agent.Workspace, "libs/a", "libs/a/deep"); got != want1 {
		t.Errorf("agent one differs:\n%s\nwant\n%s", got, want1)
	}
	if got := snapshot(t, m2.Agent.Workspace); got != want2 {
		t.Errorf("agent two differs:\n%s\nwant\n%s", got, want2)
	}
	if got := snapshot(t, sib, "libs/a"); got != sibBefore {
		t.Errorf("sibling changed:\n%s\nwas\n%s", got, sibBefore)
	}
	if treeHash(t, sibModules) != sibTree {
		t.Error("the sibling's modules tree changed")
	}
}

func TestReceiveCopyCreatesBranch(t *testing.T) {
	ctx := context.Background()
	ws := gittest.NewRepo(t)
	gittest.Write(t, filepath.Join(ws, "a.txt"), "dirty\n")
	before := gittest.Run(t, ws, "status", "--porcelain=v2")
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	if err := ForkPlainBranch(ctx, ws, "main-web-copy", "web-copy"); err != nil {
		t.Fatalf("ForkPlainBranch: %v", err)
	}
	if got := gittest.Run(t, ws, "symbolic-ref", "HEAD"); got != "refs/heads/main-web-copy" {
		t.Errorf("HEAD = %s", got)
	}
	if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := gittest.Run(t, ws, "status", "--porcelain=v2"); got != before {
		t.Errorf("working state changed: %s", got)
	}
	if err := ForkPlainBranch(ctx, ws, "main-web-copy", "web-copy"); err != nil {
		t.Errorf("a re-run on its own branch: %v", err)
	}
	gittest.Run(t, ws, "branch", "taken")
	if err := ForkPlainBranch(ctx, ws, "taken", "web-copy"); !HasCode(err, CodeBranchExists) {
		t.Errorf("existing branch = %v, want branch_exists", err)
	}
	if err := ForkPlainBranch(ctx, t.TempDir(), "x", "web-copy"); err != nil {
		t.Errorf("non-git workspace = %v", err)
	}
}
