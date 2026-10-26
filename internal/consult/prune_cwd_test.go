package consult

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var cwdTestNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// cwdScanFake is a processCwds seam that counts how often it runs.
type cwdScanFake struct {
	calls atomic.Int32
	procs []procCwd
	err   error
}

func (f *cwdScanFake) scan(context.Context) ([]procCwd, error) {
	f.calls.Add(1)
	return f.procs, f.err
}

func pruneWithCwds(t *testing.T, stateDir string, fake *cwdScanFake, dryRun bool) PruneReport {
	t.Helper()
	report, err := PruneWorktrees(stateDir, PruneOptions{DryRun: dryRun, now: func() time.Time { return cwdTestNow }, processCwds: fake.scan})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func seedDeferred(t *testing.T, repo, stateDir, id string, mode Mode, endedAgo time.Duration) Record {
	t.Helper()
	return seedIsolated(t, repo, stateDir, id, func(r *Record) {
		r.Mode, r.Status, r.WorktreeState = mode, StatusClosed, WorktreeDeferred
		if mode == ModeHeadless {
			r.Status = StatusDone
		}
		r.EndedAt = cwdTestNow.Add(-endedAgo)
	})
}

func TestPruneReleasesQuietDeferredWorktreesNoProcessWorksIn(t *testing.T) {
	for _, mode := range []Mode{ModeInteractive, ModeHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			repo, stateDir := gitTestRepo(t), t.TempDir()
			rec := seedDeferred(t, repo, stateDir, "d-quiet", mode, 48*time.Hour)
			fake := &cwdScanFake{procs: []procCwd{{PID: 1, Comm: "launchd", Cwd: "/"}}}

			if got := itemsByID(pruneWithCwds(t, stateDir, fake, true))[rec.ID]; got.Action != PruneRemove {
				t.Fatalf("dry run: action = %q (%s), want remove", got.Action, got.Reason)
			}
			real := itemsByID(pruneWithCwds(t, stateDir, fake, false))[rec.ID]
			if !real.Removed || dirExists(rec.Worktree) {
				t.Fatalf("real run: removed=%v dir=%v (%s)", real.Removed, dirExists(rec.Worktree), real.Error)
			}
		})
	}
}

func TestPruneKeepsQuietDeferredWorktreeWithUncommittedWork(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedDeferred(t, repo, stateDir, "d-wip", ModeInteractive, 48*time.Hour)
	if err := os.WriteFile(filepath.Join(rec.Worktree, "wip.txt"), []byte("wip"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &cwdScanFake{procs: []procCwd{{PID: 1, Cwd: "/"}}}

	if got := itemsByID(pruneWithCwds(t, stateDir, fake, true))[rec.ID].Action; got != PruneKeepDirty {
		t.Fatalf("action = %q, want %q", got, PruneKeepDirty)
	}
}

func TestPruneKeepsDeferredWorktreeAProcessWorksIn(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedDeferred(t, repo, stateDir, "d-1", ModeInteractive, 48*time.Hour)
	// An empty directory leaves the worktree clean; live processes' cwds exist.
	if err := os.MkdirAll(filepath.Join(rec.Worktree, "pkg", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(stateDir, alias); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cwd  string
		busy bool
	}{
		{"cwd is the worktree", rec.Worktree, true},
		{"cwd is below the worktree", filepath.Join(rec.Worktree, "pkg", "x"), true},
		{"cwd reached through a symlink", filepath.Join(alias, worktreesDirName, "d-1", "pkg", "x"), true},
		{"sibling sharing a name prefix", rec.Worktree + "0", false},
		{"parent directory", filepath.Dir(rec.Worktree), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &cwdScanFake{procs: []procCwd{{PID: 4321, Comm: "node", Cwd: tc.cwd}}}
			got := itemsByID(pruneWithCwds(t, stateDir, fake, true))[rec.ID]
			if tc.busy {
				if got.Action != PruneKeepDeferred || !strings.Contains(got.Reason, "process 4321 (node) working in worktree") {
					t.Fatalf("got %q (%s), want keep:deferred naming the process", got.Action, got.Reason)
				}
			} else if got.Action != PruneRemove {
				t.Fatalf("got %q (%s), want remove", got.Action, got.Reason)
			}
		})
	}
}

func TestPruneKeepsDeferredWorktreeWhenQuietnessIsNotProven(t *testing.T) {
	cases := []struct {
		name    string
		endedAt func(time.Time) time.Time
		fake    func() *cwdScanFake
		reason  string
		scans   int32
	}{
		{"ended under 24h ago", func(n time.Time) time.Time { return n.Add(-23 * time.Hour) }, func() *cwdScanFake { return &cwdScanFake{procs: []procCwd{{PID: 1, Cwd: "/"}}} }, "ended < 24h ago", 0},
		{"no end time", func(time.Time) time.Time { return time.Time{} }, func() *cwdScanFake { return &cwdScanFake{procs: []procCwd{{PID: 1, Cwd: "/"}}} }, "no end time recorded", 0},
		{"scan failed", func(n time.Time) time.Time { return n.Add(-48 * time.Hour) }, func() *cwdScanFake { return &cwdScanFake{err: errors.New("lsof blew up")} }, "cwd scan unavailable: lsof blew up", 1},
		{"scan saw nothing", func(n time.Time) time.Time { return n.Add(-48 * time.Hour) }, func() *cwdScanFake { return &cwdScanFake{} }, "cwd scan unavailable", 1},
	}
	for _, mode := range []Mode{ModeInteractive, ModeHeadless} {
		for _, tc := range cases {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				repo, stateDir := gitTestRepo(t), t.TempDir()
				rec := seedIsolated(t, repo, stateDir, "d-keep", func(r *Record) {
					r.Mode, r.Status, r.WorktreeState, r.EndedAt = mode, StatusFailed, WorktreeDeferred, tc.endedAt(cwdTestNow)
				})

				if rec.EndedAt = tc.endedAt(cwdTestNow); rec.EndedAt.IsZero() {
					// Closing a handle stamps a missing end time; write it back unset.
					if err := writeRecord(Dir(stateDir), rec); err != nil {
						t.Fatal(err)
					}
				}
				fake := tc.fake()
				got := itemsByID(pruneWithCwds(t, stateDir, fake, true))[rec.ID]
				if got.Action != PruneKeepDeferred || !strings.Contains(got.Reason, tc.reason) {
					t.Fatalf("got %q (%s), want keep:deferred containing %q", got.Action, got.Reason, tc.reason)
				}
				if n := fake.calls.Load(); n != tc.scans {
					t.Fatalf("scans = %d, want %d", n, tc.scans)
				}
			})
		}
	}
}

func TestPruneScansProcessesOnceAcrossRecordsAndOnlyWhenNeeded(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	for _, id := range []string{"d-a", "d-b", "d-c", "d-d", "d-e"} {
		seedDeferred(t, repo, stateDir, id, ModeInteractive, 48*time.Hour)
	}
	fake := &cwdScanFake{procs: []procCwd{{PID: 1, Cwd: "/"}}}
	pruneWithCwds(t, stateDir, fake, true)
	if n := fake.calls.Load(); n != 1 {
		t.Fatalf("scans = %d across 5 records, want 1", n)
	}

	noneDeferred, idle := t.TempDir(), &cwdScanFake{}
	seedIsolated(t, gitTestRepo(t), noneDeferred, "d-kept", nil)
	pruneWithCwds(t, noneDeferred, idle, true)
	if n := idle.calls.Load(); n != 0 {
		t.Fatalf("scans = %d with no deferred record, want 0", n)
	}
}

func TestPruneKeepsRecordsTheLegacyChecksAlreadyRelease(t *testing.T) {
	// A released interactive run needs no scan at all.
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-rel", func(r *Record) {
		r.Status, r.WorktreeState, r.EndedAt = StatusReleased, WorktreeDeferred, cwdTestNow.Add(-time.Minute)
	})
	fake := &cwdScanFake{err: errors.New("never consulted")}
	if got := itemsByID(pruneWithCwds(t, stateDir, fake, true))[rec.ID].Action; got != PruneRemove {
		t.Fatalf("action = %q, want remove", got)
	}
	if n := fake.calls.Load(); n != 0 {
		t.Fatalf("scans = %d, want 0", n)
	}
}
