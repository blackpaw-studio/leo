package consult

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func commitInWorktree(t *testing.T, wt, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, file), []byte(file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "add", file)
	runGit(t, wt, "-c", "user.email=a@b.c", "-c", "user.name=T", "commit", "-m", file)
}

// pruneFixture is a state dir holding one dispatch per safety case, each a
// real worktree of a real repository.
type pruneFixture struct {
	stateDir           string
	repo, repo2, repo3 string
	recs               map[string]Record
	orphan             string
	missingRepo        string
}

func newPruneFixture(t *testing.T) pruneFixture {
	t.Helper()
	f := pruneFixture{stateDir: t.TempDir(), repo: gitTestRepo(t), repo2: gitTestRepo(t), repo3: gitTestRepo(t), recs: map[string]Record{}}
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := func(id, repo string, edit func(*Record)) Record {
		rec := seedIsolated(t, repo, f.stateDir, id, edit)
		f.recs[id] = rec
		return rec
	}
	seed("d-base", f.repo, nil)
	seed("d-base2", f.repo2, nil)
	merged := seed("d-merged", f.repo, nil)
	commitInWorktree(t, merged.Worktree, "merged.txt")
	runGit(t, f.repo, "merge", "--ff-only", merged.Branch)
	ignored := seed("d-ignored", f.repo, nil)
	if err := os.MkdirAll(filepath.Join(ignored.Worktree, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignored.Worktree, "build", "out.bin"), make([]byte, 64<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := seed("d-dirty", f.repo, nil)
	if err := os.WriteFile(filepath.Join(dirty.Worktree, "wip.txt"), []byte("wip"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmerged := seed("d-unmerged", f.repo, nil)
	commitInWorktree(t, unmerged.Worktree, "unmerged.txt")
	seed("d-running", f.repo, func(r *Record) { r.Status, r.PaneID, r.EndedAt = StatusRunning, "%3", time.Time{} })
	seed("d-idle", f.repo, func(r *Record) { r.Status, r.PaneID, r.EndedAt = StatusIdle, "%4", time.Time{} })
	seed("d-pane", f.repo, func(r *Record) { r.Status, r.PaneID = StatusClosed, "%5" })
	seed("d-restarted", f.repo, func(r *Record) { r.Mode, r.Status, r.Error = ModeHeadless, StatusFailed, "daemon restarted" })
	missing := seed("d-norepo", f.repo3, nil)
	f.missingRepo = missing.ID
	// Records whose worktree directory is already gone (deleted by hand, so
	// git still lists the checkout as prunable): one in a live repository,
	// one whose repository is gone too.
	gone := seed("d-gone", f.repo, nil)
	if err := os.RemoveAll(gone.Worktree); err != nil {
		t.Fatal(err)
	}
	goneNoRepo := seed("d-gone-norepo", f.repo3, nil)
	if err := os.RemoveAll(goneNoRepo.Worktree); err != nil {
		t.Fatal(err)
	}
	// A removed record with no directory is simply done, and a running one is
	// not prune's business: neither is listed.
	seed("d-done", f.repo, func(r *Record) { r.WorktreeState = WorktreeRemoved })
	if err := os.RemoveAll(f.recs["d-done"].Worktree); err != nil {
		t.Fatal(err)
	}
	seed("d-live-gone", f.repo, func(r *Record) { r.Status, r.PaneID, r.EndedAt = StatusRunning, "%9", time.Time{} })
	if err := os.RemoveAll(f.recs["d-live-gone"].Worktree); err != nil {
		t.Fatal(err)
	}
	f.orphan = filepath.Join(f.stateDir, worktreesDirName, "d-orphan")
	if err := os.MkdirAll(f.orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.orphan, "keep.txt"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// breakRepo makes a dispatch's canonical repository disappear.
func (f pruneFixture) breakRepo(t *testing.T) {
	t.Helper()
	rec := f.recs[f.missingRepo]
	if err := os.Rename(rec.RepositoryRoot, rec.RepositoryRoot+".moved"); err != nil {
		t.Fatal(err)
	}
}

func itemsByID(r PruneReport) map[string]PruneItem {
	out := map[string]PruneItem{}
	for _, it := range r.Items {
		out[it.ID] = it
	}
	return out
}

// TestPruneWorktrees runs the dry run and then the real run over one shared
// fixture: building a dozen real worktrees dominates the cost of either.
func TestPruneWorktrees(t *testing.T) {
	f := newPruneFixture(t)
	f.breakRepo(t)
	t.Run("dry run classifies every worktree and changes nothing", func(t *testing.T) { dryRunChangesNothing(t, f) })
	t.Run("real run removes only what the safety rules allow", func(t *testing.T) { realRunHonoursSafetyRules(t, f) })
}

func dryRunChangesNothing(t *testing.T, f pruneFixture) {
	var mu sync.Mutex
	var argvs [][]string
	opts := PruneOptions{DryRun: true, GitCommand: recordingGit(&mu, &argvs)}

	report, err := PruneWorktrees(f.stateDir, opts)
	if err != nil {
		t.Fatal(err)
	}

	got := itemsByID(report)
	want := map[string]string{
		"d-base": "remove", "d-base2": "remove", "d-merged": "remove", "d-ignored": "remove",
		"d-dirty": "keep:dirty", "d-unmerged": "keep:unmerged",
		"d-running": "keep:not-terminal", "d-idle": "keep:not-terminal", "d-pane": "keep:not-terminal",
		"d-restarted": "keep:unverifiable",
		"d-norepo":    "missing-repo",
		"d-gone":      "stale-record", "d-gone-norepo": "stale-record",
		"d-orphan": "orphan",
	}
	for id, action := range want {
		if got[id].Action != action {
			t.Errorf("%s action = %q, want %q (%+v)", id, got[id].Action, action, got[id])
		}
	}
	for _, id := range []string{"d-done", "d-live-gone"} {
		if _, listed := got[id]; listed {
			t.Errorf("%s has no worktree to reconcile but was listed: %+v", id, got[id])
		}
	}
	for _, id := range []string{"d-gone", "d-gone-norepo"} {
		if got[id].Bytes != 0 {
			t.Errorf("%s stale record has size %d, want 0", id, got[id].Bytes)
		}
	}
	if report.Totals["stale-record"].Count != 2 || report.Totals["stale-record"].Bytes != 0 {
		t.Errorf("stale-record total = %+v", report.Totals["stale-record"])
	}
	if len(got) != len(want) {
		t.Errorf("items = %d, want %d: %+v", len(got), len(want), report.Items)
	}
	var reclaimable int64
	for _, id := range []string{"d-base", "d-base2", "d-merged", "d-ignored"} {
		reclaimable += got[id].Bytes
	}
	if got["d-ignored"].Bytes < 64<<10 {
		t.Errorf("d-ignored size = %d, want at least its 64 KiB of build output", got["d-ignored"].Bytes)
	}
	if report.Reclaimable.Count != 4 || report.Reclaimable.Bytes != reclaimable {
		t.Errorf("reclaimable = %+v, want 4 / %d", report.Reclaimable, reclaimable)
	}
	if report.Totals["remove"].Count != 4 || report.Totals["keep:dirty"].Count != 1 || report.Totals["orphan"].Count != 1 {
		t.Errorf("totals = %+v", report.Totals)
	}
	if !report.DryRun || report.Reclaimed.Count != 0 {
		t.Errorf("dry run reported work done: %+v", report)
	}
	for id, rec := range f.recs {
		if !dirExists(rec.Worktree) && !slices.Contains([]string{"d-gone", "d-gone-norepo", "d-done", "d-live-gone"}, id) {
			t.Errorf("dry run removed %s", id)
		}
	}
	if !branchExists(t, f.repo, f.recs["d-gone"].Branch) {
		t.Error("dry run deleted a stale record's branch")
	}
	if _, err := os.Stat(filepath.Join(f.orphan, "keep.txt")); err != nil {
		t.Errorf("orphan touched: %v", err)
	}
	for _, argv := range argvs {
		if mutatingGit(argv) {
			t.Errorf("dry run ran a mutating git command: %q", argv)
		}
	}
	for id := range want {
		if id == "d-orphan" {
			continue
		}
		persisted, err := LoadOne(f.stateDir, id)
		if err != nil || persisted.WorktreeState != f.recs[id].WorktreeState {
			t.Errorf("dry run changed record %s: %+v %v", id, persisted.WorktreeState, err)
		}
	}
}

func realRunHonoursSafetyRules(t *testing.T, f pruneFixture) {
	var mu sync.Mutex
	var argvs [][]string

	report, err := PruneWorktrees(f.stateDir, PruneOptions{GitCommand: recordingGit(&mu, &argvs)})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"d-base", "d-base2", "d-merged", "d-ignored"} {
		rec := f.recs[id]
		if dirExists(rec.Worktree) {
			t.Errorf("%s: worktree not removed", id)
		}
		persisted, _ := LoadOne(f.stateDir, id)
		if persisted.WorktreeState != WorktreeRemoved {
			t.Errorf("%s: record state = %q", id, persisted.WorktreeState)
		}
		if branchExists(t, f.recs[id].RepositoryRoot, rec.Branch) {
			t.Errorf("%s: merged branch %s not deleted", id, rec.Branch)
		}
	}
	for _, id := range []string{"d-gone", "d-gone-norepo"} {
		persisted, _ := LoadOne(f.stateDir, id)
		if persisted.WorktreeState != WorktreeRemoved {
			t.Errorf("%s: stale record state = %q, want removed", id, persisted.WorktreeState)
		}
	}
	if branchExists(t, f.repo, f.recs["d-gone"].Branch) {
		t.Error("d-gone: its branch is merged elsewhere and should be deleted once the stale checkout is pruned")
	}
	for _, id := range []string{"d-dirty", "d-unmerged", "d-running", "d-idle", "d-pane", "d-restarted", "d-norepo"} {
		rec := f.recs[id]
		if !dirExists(rec.Worktree) {
			t.Errorf("%s: worktree was removed", id)
		}
	}
	if !branchExists(t, f.repo, f.recs["d-unmerged"].Branch) || !branchExists(t, f.repo, f.recs["d-dirty"].Branch) {
		t.Error("a branch holding work was deleted")
	}
	if _, err := os.Stat(filepath.Join(f.orphan, "keep.txt")); err != nil {
		t.Errorf("orphan touched: %v", err)
	}
	if report.Reclaimed.Count != 6 || itemsByID(report)["d-norepo"].Removed {
		t.Errorf("reclaimed = %+v items=%+v", report.Reclaimed, itemsByID(report)["d-base2"])
	}
	// d-norepo's repository is gone: nothing to prune there.
	for _, repo := range []string{f.repo, f.recs["d-base2"].RepositoryRoot} {
		want := []string{"git", "-C", repo, "worktree", "prune"}
		n := 0
		for _, argv := range argvs {
			if slices.Equal(argv, want) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("git worktree prune ran %d times for %s, want once: %q", n, repo, argvs)
		}
	}
	for _, argv := range argvs {
		if slices.Contains(argv, "--force") || slices.Contains(argv, "-f") {
			t.Errorf("forced git: %q", argv)
		}
		if mutatingGit(argv) && argv[2] != f.repo && argv[2] != f.repo2 {
			t.Errorf("mutating git outside a canonical repository: %q", argv)
		}
	}

	again, err := PruneWorktrees(f.stateDir, PruneOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Totals["stale-record"].Count != 0 {
		t.Errorf("second pass still finds stale records: %+v", again.Totals)
	}
	if again.Reclaimable.Count != 0 {
		t.Errorf("second pass still finds %d removable: %+v", again.Reclaimable.Count, again.Items)
	}
}

func TestPruneRechecksTheRecordBeforeRemoving(t *testing.T) {
	f := pruneFixture{stateDir: t.TempDir()}
	rec := seedIsolated(t, gitTestRepo(t), f.stateDir, "d-base", nil)
	var once sync.Once
	opts := PruneOptions{GitCommand: func(name string, args ...string) *exec.Cmd {
		// The dispatch comes back to life after its first assessment, before
		// the removal pass re-reads it.
		if slices.Contains(args, "status") {
			once.Do(func() {
				live := rec
				live.Status, live.EndedAt = StatusRunning, time.Time{}
				if err := writeRecord(Dir(f.stateDir), live); err != nil {
					t.Error(err)
				}
			})
		}
		return exec.Command(name, args...)
	}}

	if _, err := PruneWorktrees(f.stateDir, opts); err != nil {
		t.Fatal(err)
	}

	if !dirExists(rec.Worktree) {
		t.Fatal("a dispatch that became active before removal lost its worktree")
	}
}

// mutatingGit reports whether argv (git -C <dir> ...) changes repository state.
func mutatingGit(argv []string) bool {
	for _, verb := range [][]string{{"worktree", "remove"}, {"worktree", "prune"}, {"worktree", "add"}, {"branch", "-D"}, {"branch", "-d"}, {"update-ref"}, {"reset"}, {"clean"}, {"checkout"}} {
		for i := 0; i+len(verb) <= len(argv); i++ {
			if slices.Equal(argv[i:i+len(verb)], verb) {
				return true
			}
		}
	}
	return slices.Contains(argv, "gc")
}

func recordingGit(mu *sync.Mutex, argvs *[][]string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		*argvs = append(*argvs, append([]string{name}, args...))
		mu.Unlock()
		if name != "git" {
			panic("prune ran " + strings.Join(append([]string{name}, args...), " "))
		}
		return exec.Command(name, args...)
	}
}
