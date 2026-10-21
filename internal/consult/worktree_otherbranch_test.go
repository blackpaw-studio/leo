package consult

import (
	"os"
	"path/filepath"
	"testing"
)

// otherBranchCase checks a dispatch worktree whose checkout was moved off its
// own leo/<id> branch. mutate runs in the worktree, after it has been moved.
type otherBranchCase struct {
	name       string
	prepare    func(t *testing.T, repo string, rec Record) // while still on leo/<id>
	moveTo     func(t *testing.T, rec Record)
	mutate     func(t *testing.T, repo string, rec Record)
	wantState  WorktreeState
	wantBranch bool // leo/<id> still exists afterwards
	wantNamed  bool // the named branch still exists afterwards
}

func TestWorktreeOnAnotherBranch(t *testing.T) {
	const named = "feature"
	switchNamed := func(t *testing.T, rec Record) { runGit(t, rec.Worktree, "switch", "-q", "-c", named) }
	cases := []otherBranchCase{
		{
			name:   "named branch merged into main is removed and the branch survives",
			moveTo: switchNamed,
			mutate: func(t *testing.T, repo string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
				runGit(t, repo, "merge", "--ff-only", named)
			},
			wantState: WorktreeRemoved, wantBranch: false, wantNamed: true,
		},
		{
			name:   "named branch with a commit only on itself is kept unmerged",
			moveTo: switchNamed,
			mutate: func(t *testing.T, _ string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
			},
			wantState: WorktreeKept, wantBranch: true, wantNamed: true,
		},
		{
			name: "commit reachable only from leo/<id> and the named branch is kept unmerged",
			prepare: func(t *testing.T, _ string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
			},
			moveTo:    switchNamed,
			wantState: WorktreeKept, wantBranch: true, wantNamed: true,
		},
		{
			name:   "dirty named branch is kept",
			moveTo: switchNamed,
			mutate: func(t *testing.T, repo string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
				runGit(t, repo, "merge", "--ff-only", named)
				if err := os.WriteFile(filepath.Join(rec.Worktree, "scratch.txt"), []byte("x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantState: WorktreeKept, wantBranch: true, wantNamed: true,
		},
		{
			name:   "detached HEAD at a merged commit is removed",
			moveTo: func(t *testing.T, rec Record) { runGit(t, rec.Worktree, "switch", "-q", "--detach") },
			mutate: func(t *testing.T, repo string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
				runGit(t, repo, "merge", "--ff-only", runGit(t, rec.Worktree, "rev-parse", "HEAD"))
			},
			wantState: WorktreeRemoved, wantBranch: false, wantNamed: false,
		},
		{
			name:   "detached HEAD with a commit nothing else reaches is kept unmerged",
			moveTo: func(t *testing.T, rec Record) { runGit(t, rec.Worktree, "switch", "-q", "--detach") },
			mutate: func(t *testing.T, _ string, rec Record) {
				commitInWorktree(t, rec.Worktree, "work.txt")
			},
			wantState: WorktreeKept, wantBranch: true, wantNamed: false,
		},
		{
			name: "leo/<id> holding unmerged commits survives removal of a merged worktree",
			prepare: func(t *testing.T, _ string, rec Record) {
				commitInWorktree(t, rec.Worktree, "only-on-leo.txt")
			},
			moveTo: func(t *testing.T, rec Record) {
				runGit(t, rec.Worktree, "switch", "-q", "-c", named, rec.BaseCommit)
			},
			wantState: WorktreeRemoved, wantBranch: true, wantNamed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, stateDir := gitTestRepo(t), t.TempDir()
			rec := seedIsolated(t, repo, stateDir, "d-other", nil)
			if tc.prepare != nil {
				tc.prepare(t, repo, rec)
			}
			tc.moveTo(t, rec)
			if tc.mutate != nil {
				tc.mutate(t, repo, rec)
			}
			d := NewDispatcher(NewFileRecorder(stateDir))
			d.SetInteractiveRuntime(newPaneRuntime(false))

			got := d.Collect(rec)

			if got.WorktreeState != tc.wantState {
				t.Fatalf("worktree state = %q, want %q (%s)", got.WorktreeState, tc.wantState, assessWorktree(d.git, rec).Reason)
			}
			if gone := !dirExists(rec.Worktree); gone != (tc.wantState == WorktreeRemoved) {
				t.Fatalf("worktree directory gone = %v", gone)
			}
			if has := branchExists(t, repo, rec.Branch); has != tc.wantBranch {
				t.Fatalf("leo branch exists = %v, want %v", has, tc.wantBranch)
			}
			if has := branchExists(t, repo, named); has != tc.wantNamed {
				t.Fatalf("named branch exists = %v, want %v", has, tc.wantNamed)
			}
		})
	}
}

func TestAnotherBranchVerdicts(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-verdict", nil)
	runGit(t, rec.Worktree, "switch", "-q", "-c", "feature")
	d := NewDispatcher(NewFileRecorder(stateDir))
	commitInWorktree(t, rec.Worktree, "work.txt")
	if got := assessWorktree(d.git, rec).Verdict; got != verdictUnmerged {
		t.Fatalf("verdict = %q, want keep:unmerged", got)
	}
	if err := os.WriteFile(filepath.Join(rec.Worktree, "scratch.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := assessWorktree(d.git, rec).Verdict; got != verdictDirty {
		t.Fatalf("verdict = %q, want keep:dirty", got)
	}
}
