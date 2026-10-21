package consult

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedIsolated creates a real worktree under stateDir/worktrees and persists
// a terminal interactive record for it, as a finished dispatch leaves behind.
func seedIsolated(t *testing.T, repo, stateDir, id string, edit func(rec *Record)) Record {
	t.Helper()
	base := runGit(t, repo, "rev-parse", "HEAD")
	wt := filepath.Join(stateDir, worktreesDirName, id)
	branch := "leo/" + id
	runGit(t, repo, "worktree", "add", "-b", branch, wt, base)
	rec := Record{
		ID: id, Template: "claude", Harness: "claude", Model: "opus", Kind: "dispatch", Mode: ModeInteractive,
		Status: StatusDone, StartedAt: time.Now(), EndedAt: time.Now(), Cwd: wt,
		Isolation: "worktree", Worktree: wt, Branch: branch, BaseCommit: base, SourceCwd: repo, RepositoryRoot: repo,
		WorktreeState: WorktreeKept,
	}
	if edit != nil {
		edit(&rec)
	}
	h, err := NewFileRecorder(stateDir).Open(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(rec.Status, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// paneRuntime is an interactive runtime that records the exact pane each
// Kill names, with a scriptable liveness probe.
type paneRuntime struct {
	*fakeInteractiveRuntime
	mu2      sync.Mutex
	killed   []string
	probeErr error
}

func newPaneRuntime(alive bool) *paneRuntime {
	return &paneRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: alive}}
}

func (r *paneRuntime) Kill(pane string) error {
	r.mu2.Lock()
	r.killed = append(r.killed, pane)
	r.mu2.Unlock()
	return r.fakeInteractiveRuntime.Kill(pane)
}

func (r *paneRuntime) PaneAlive(string) (bool, error) {
	if r.probeErr != nil {
		return false, r.probeErr
	}
	return r.Alive(""), nil
}

func (r *paneRuntime) killedPanes() []string {
	r.mu2.Lock()
	defer r.mu2.Unlock()
	return slices.Clone(r.killed)
}

// recordGit wraps the dispatcher's git seam, capturing every argv.
func recordGit(d *Dispatcher) *[][]string {
	var mu sync.Mutex
	var calls [][]string
	d.GitCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string{name}, args...))
		mu.Unlock()
		return exec.Command(name, args...)
	}
	return &calls
}

func TestReleaseRemovesACleanWorktreeOnceThePaneIsGone(t *testing.T) {
	for _, initial := range []WorktreeState{WorktreePresent, WorktreeKept, WorktreeDeferred} {
		t.Run(string(initial), func(t *testing.T) {
			repo, stateDir := gitTestRepo(t), t.TempDir()
			rec := seedIsolated(t, repo, stateDir, "d-rel1", func(r *Record) {
				r.Status, r.PaneID, r.WorktreeState = StatusIdle, "%7", initial
				r.EndedAt = time.Time{}
			})
			rt := newPaneRuntime(true)
			d := NewDispatcher(NewFileRecorder(stateDir))
			d.SetInteractiveRuntime(rt)
			calls := recordGit(d)

			got, err := d.Release(rec.ID)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(rt.killedPanes(), []string{"%7"}) {
				t.Fatalf("killed panes = %q", rt.killedPanes())
			}
			if got.Status != StatusReleased || got.WorktreeState != WorktreeRemoved {
				t.Fatalf("status=%q worktree=%q", got.Status, got.WorktreeState)
			}
			if dirExists(rec.Worktree) {
				t.Fatal("worktree directory survived the release")
			}
			if branchExists(t, repo, rec.Branch) {
				t.Fatal("branch at the base commit should be deleted: its tip is reachable from the default branch")
			}
			persisted, _ := LoadOne(stateDir, rec.ID)
			if persisted.WorktreeState != WorktreeRemoved {
				t.Fatalf("persisted worktree state = %q", persisted.WorktreeState)
			}
			want := [][]string{
				{"git", "-C", repo, "worktree", "remove", rec.Worktree},
				{"git", "-C", repo, "branch", "-D", rec.Branch},
			}
			for _, w := range want {
				if !containsArgv(*calls, w) {
					t.Fatalf("missing git argv %q in %q", w, *calls)
				}
			}
			for _, c := range *calls {
				if len(c) > 2 && (slices.Contains(c, "remove") || slices.Contains(c, "branch")) && c[2] != repo {
					t.Fatalf("mutating git ran outside the canonical repository: %q", c)
				}
				if slices.Contains(c, "--force") || slices.Contains(c, "-f") {
					t.Fatalf("forced git: %q", c)
				}
			}
		})
	}
}

func containsArgv(calls [][]string, want []string) bool {
	for _, c := range calls {
		if slices.Equal(c, want) {
			return true
		}
	}
	return false
}

func TestReleaseKeepsWorktreesHoldingWork(t *testing.T) {
	cases := map[string]func(t *testing.T, repo string, rec Record){
		"modified tracked file": func(t *testing.T, _ string, rec Record) {
			if err := os.WriteFile(filepath.Join(rec.Worktree, "tracked.txt"), []byte("edited\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"untracked file": func(t *testing.T, _ string, rec Record) {
			if err := os.WriteFile(filepath.Join(rec.Worktree, "new.txt"), []byte("x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unmerged commit": func(t *testing.T, _ string, rec Record) {
			if err := os.WriteFile(filepath.Join(rec.Worktree, "work.txt"), []byte("w\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, rec.Worktree, "add", "work.txt")
			runGit(t, rec.Worktree, "-c", "user.email=a@b.c", "-c", "user.name=T", "commit", "-m", "work")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			repo, stateDir := gitTestRepo(t), t.TempDir()
			rec := seedIsolated(t, repo, stateDir, "d-keep1", func(r *Record) { r.Status, r.PaneID = StatusIdle, "%7" })
			mutate(t, repo, rec)
			rt := newPaneRuntime(true)
			d := NewDispatcher(NewFileRecorder(stateDir))
			d.SetInteractiveRuntime(rt)

			got, err := d.Release(rec.ID)
			if err != nil {
				t.Fatal(err)
			}

			if got.WorktreeState != WorktreeKept || !dirExists(rec.Worktree) || !branchExists(t, repo, rec.Branch) {
				t.Fatalf("state=%q dir=%v branch=%v", got.WorktreeState, dirExists(rec.Worktree), branchExists(t, repo, rec.Branch))
			}
		})
	}
}

func TestCleanupIgnoresIgnoredBuildOutput(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := seedIsolated(t, repo, stateDir, "d-ign1", nil)
	if err := os.MkdirAll(filepath.Join(rec.Worktree, "build", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec.Worktree, "build", "deep", "out.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	got := d.Collect(rec)

	if got.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
		t.Fatalf("ignored build output blocked removal: state=%q dir=%v", got.WorktreeState, dirExists(rec.Worktree))
	}
}

func TestCleanupRemovesCommitsReachableFromAnotherRefAndDeletesTheBranch(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-merged", nil)
	if err := os.WriteFile(filepath.Join(rec.Worktree, "work.txt"), []byte("w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, rec.Worktree, "add", "work.txt")
	runGit(t, rec.Worktree, "-c", "user.email=a@b.c", "-c", "user.name=T", "commit", "-m", "work")
	tip := runGit(t, rec.Worktree, "rev-parse", "HEAD")
	runGit(t, repo, "merge", "--ff-only", rec.Branch)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	got := d.Collect(rec)

	if got.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
		t.Fatalf("merged worktree not removed: state=%q", got.WorktreeState)
	}
	if branchExists(t, repo, rec.Branch) {
		t.Fatal("merged branch should be deleted")
	}
	if got.BranchTip != tip {
		t.Fatalf("BranchTip = %q, want %q", got.BranchTip, tip)
	}
}

func TestCleanupKeepsTheBranchWhenNothingElseReachesItsTip(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-orph", nil)
	// Move the base so that only the leo branch still reaches it.
	runGit(t, repo, "checkout", "-q", "--orphan", "other")
	runGit(t, repo, "rm", "-rfq", ".")
	runGit(t, repo, "-c", "user.email=a@b.c", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "elsewhere")
	for _, head := range strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")) {
		if head != "other" && head != rec.Branch {
			runGit(t, repo, "branch", "-D", head)
		}
	}
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	got := d.Collect(rec)

	if got.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
		t.Fatalf("clean worktree at base must be removed: state=%q", got.WorktreeState)
	}
	if !branchExists(t, repo, rec.Branch) {
		t.Fatal("branch holding the only reference to the base commit was deleted")
	}
}

func TestPaneAliveDefersCleanupAndALaterCollectRetries(t *testing.T) {
	cases := map[string]func(rt *paneRuntime){
		"alive":              func(rt *paneRuntime) { rt.setAlive(true) },
		"liveness uncertain": func(rt *paneRuntime) { rt.probeErr = errors.New("tmux unreachable") },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			repo, stateDir := gitTestRepo(t), t.TempDir()
			rec := seedIsolated(t, repo, stateDir, "d-defer", func(r *Record) { r.PaneID, r.WorktreeState = "%7", WorktreePresent })
			rt := newPaneRuntime(true)
			setup(rt)
			d := NewDispatcher(NewFileRecorder(stateDir))
			d.SetInteractiveRuntime(rt)

			first := d.Collect(rec)
			if first.WorktreeState != WorktreeDeferred || !dirExists(rec.Worktree) {
				t.Fatalf("first collect: state=%q dir=%v (want deferred, kept on disk)", first.WorktreeState, dirExists(rec.Worktree))
			}

			rt.probeErr = nil
			rt.setAlive(false)
			second := d.Collect(first)
			if second.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
				t.Fatalf("retry after the pane died: state=%q dir=%v", second.WorktreeState, dirExists(rec.Worktree))
			}
		})
	}
}

func TestRestartClosesTheRecordedPaneThenCleansItsWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-rst1", func(r *Record) { r.Status, r.PaneID = StatusClosed, "%7" })
	rt := newPaneRuntime(true)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(rt)

	d.MarkInterrupted()

	got, _ := d.Get(rec.ID)
	if !slices.Equal(rt.killedPanes(), []string{"%7"}) {
		t.Fatalf("killed panes = %q", rt.killedPanes())
	}
	if got.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
		t.Fatalf("state=%q dir=%v", got.WorktreeState, dirExists(rec.Worktree))
	}
}

func TestRestartRetriesADeferredCleanupWhenThePaneIsAbsent(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-rst2", func(r *Record) { r.Status, r.WorktreeState = StatusReleased, WorktreeDeferred })
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	d.MarkInterrupted()

	got, _ := d.Get(rec.ID)
	if got.WorktreeState != WorktreeRemoved || dirExists(rec.Worktree) {
		t.Fatalf("state=%q dir=%v", got.WorktreeState, dirExists(rec.Worktree))
	}
}

func TestRestartKeepsADeferredCleanupWhenTheKillFails(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-rst3", func(r *Record) { r.Status, r.PaneID = StatusClosed, "%7" })
	rt := newPaneRuntime(true)
	rt.killErr = errors.New("kill failed")
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(rt)

	d.MarkInterrupted()

	got, _ := d.Get(rec.ID)
	if got.WorktreeState != WorktreeDeferred || !dirExists(rec.Worktree) {
		t.Fatalf("a worktree whose pane could not be killed must stay deferred: state=%q dir=%v", got.WorktreeState, dirExists(rec.Worktree))
	}
}

func TestFollowUpRecreatesAWorktreeWhoseBranchCleanupDeleted(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-flw1", nil)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))
	removed := d.Collect(rec)
	if removed.WorktreeState != WorktreeRemoved || branchExists(t, repo, rec.Branch) {
		t.Fatalf("setup: state=%q branch=%v", removed.WorktreeState, branchExists(t, repo, rec.Branch))
	}

	cwd, recreate, err := d.resumeWorkspace(removed)
	if err != nil || !recreate || cwd != rec.Worktree {
		t.Fatalf("resumeWorkspace = %q, %v, %v", cwd, recreate, err)
	}
	if err := d.addWorktree(removed); err != nil {
		t.Fatal(err)
	}
	if head := runGit(t, rec.Worktree, "rev-parse", "HEAD"); head != rec.BaseCommit {
		t.Fatalf("recreated worktree HEAD = %s, want base %s", head, rec.BaseCommit)
	}
	if got := runGit(t, rec.Worktree, "symbolic-ref", "--short", "HEAD"); got != rec.Branch {
		t.Fatalf("recreated branch = %q", got)
	}
}

// brokenRef writes a garbage loose ref, which makes git print
// "warning: ignoring broken ref ..." on stderr while for-each-ref still exits 0.
func brokenRef(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, ".git", "refs", "heads", "garbage")
	if err := os.WriteFile(path, []byte("not a sha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBrokenRefWarningDoesNotCountAsAnotherRefReachingTheCommit(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-brk1", nil)
	commitInWorktree(t, rec.Worktree, "unmerged.txt")
	brokenRef(t, repo)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	got := d.Collect(rec)

	if got.WorktreeState != WorktreeKept || !dirExists(rec.Worktree) {
		t.Fatalf("unmerged work removed on a stderr warning: state=%q dir=%v", got.WorktreeState, dirExists(rec.Worktree))
	}
	if !branchExists(t, repo, rec.Branch) {
		t.Fatal("branch holding unmerged work was deleted")
	}
	if assessed := assessWorktree(d.git, rec); assessed.Verdict != verdictUnmerged {
		t.Fatalf("verdict = %q (%s), want keep:unmerged", assessed.Verdict, assessed.Reason)
	}
}

func TestGitOutputParsedByCleanupExcludesStderr(t *testing.T) {
	d := NewDispatcher(nil)
	d.GitCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'warning: noise' >&2; echo refs/heads/main")
	}
	out, err := d.git("anything")
	if err != nil || string(out) != "refs/heads/main" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	d.GitCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'fatal: reason' >&2; exit 3")
	}
	if _, err := d.git("anything"); err == nil || !strings.Contains(err.Error(), "fatal: reason") {
		t.Fatalf("error should carry stderr: %v", err)
	}
}
