package consult

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var retentionNow = time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC)

// seedParked persists a parked, non-isolated run that ended at ended.
func seedParked(t *testing.T, stateDir, id string, ended time.Time) Record {
	t.Helper()
	rec := Record{ID: id, Kind: "dispatch", Mode: ModeInteractive, Harness: "claude", Template: "claude", Status: StatusClosed, Retained: true, SessionID: "sess-" + id, Cwd: t.TempDir(), StartedAt: ended.Add(-time.Hour), EndedAt: ended}
	recorder := NewFileRecorder(stateDir)
	if _, err := recorder.Open(rec); err != nil {
		t.Fatal(err)
	}
	if err := recorder.PersistRecord(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func retentionDispatcher(stateDir string, now time.Time) *Dispatcher {
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = func() time.Time { return now }
	d.SetInteractiveRuntime(newPaneRuntime(false))
	return d
}

func mustLoad(t *testing.T, stateDir, id string) Record {
	t.Helper()
	rec, err := LoadOne(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestRetentionReleasesExpiredParkedRun(t *testing.T) {
	stateDir := t.TempDir()
	rec := seedParked(t, stateDir, "d-expired", retentionNow.Add(-resumableRetention))
	d := retentionDispatcher(stateDir, retentionNow)

	d.SweepRetention(retentionNow)

	requireReleased(t, mustLoad(t, stateDir, rec.ID), ReleaseReasonRetention)
}

func TestRetentionKeepsDirtyWorktreeOnExpiry(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-dirtyexp", func(r *Record) {
		r.Status, r.Retained, r.WorktreeState, r.SessionID = StatusClosed, true, WorktreePresent, "sess-1"
		r.EndedAt = retentionNow.Add(-resumableRetention - time.Minute)
	})
	work := filepath.Join(rec.Worktree, "wip.txt")
	if err := os.WriteFile(work, []byte("unsaved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := retentionDispatcher(stateDir, retentionNow)

	d.SweepRetention(retentionNow)

	got := mustLoad(t, stateDir, rec.ID)
	requireReleased(t, got, ReleaseReasonRetention)
	if got.WorktreeState != WorktreeKept {
		t.Fatalf("worktree state = %q, want kept", got.WorktreeState)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "unsaved\n" {
		t.Fatalf("uncommitted work lost: %q, %v", data, err)
	}
}

func TestRetentionSkipsFreshParkedRun(t *testing.T) {
	stateDir := t.TempDir()
	rec := seedParked(t, stateDir, "d-fresh", retentionNow.Add(-resumableRetention+time.Minute))
	d := retentionDispatcher(stateDir, retentionNow)

	d.SweepRetention(retentionNow)

	if got := mustLoad(t, stateDir, rec.ID); !parked(got) {
		t.Fatalf("a fresh parked run was released: %s", got.Status)
	}
}

func TestRetentionSkipsRestartPending(t *testing.T) {
	stateDir := t.TempDir()
	rec := seedParked(t, stateDir, "d-pending", retentionNow.Add(-2*resumableRetention))
	d := retentionDispatcher(stateDir, retentionNow)
	d.MarkInterrupted() // queues the record for the restart reconcile, not yet run
	if !d.RestartPendingIDs()[rec.ID] {
		t.Fatal("test setup: the record is not pending the restart reconcile")
	}

	d.SweepRetention(retentionNow)

	if got := mustLoad(t, stateDir, rec.ID); !parked(got) {
		t.Fatalf("released a record the restart reconcile has yet to reach: %s", got.Status)
	}
}

func TestRetentionSkipsReopeningRun(t *testing.T) {
	stateDir := t.TempDir()
	rec := seedParked(t, stateDir, "d-reopening", retentionNow.Add(-2*resumableRetention))
	d := retentionDispatcher(stateDir, retentionNow)
	// A follow-up reopened the run in memory; its slot has not come up yet.
	live := cloneRecord(rec)
	live.Status, live.EndedAt = StatusQueued, time.Time{}
	live.Turns = append(live.Turns, Turn{TurnID: rec.ID + "#2", Source: TurnSourceOrchestrator, Queued: true, Text: "more"})
	state := newRunState(live, &durableTestHandle{}, make(chan struct{}), nil)
	state.awaitingSlot = true
	d.runs[rec.ID] = state

	d.SweepRetention(retentionNow)

	if got, _ := d.Get(rec.ID); got.Status != StatusQueued {
		t.Fatalf("a reopening run was touched: %s", got.Status)
	}
}

func TestRetentionSweepRunsAtMostEvery10Minutes(t *testing.T) {
	stateDir := t.TempDir()
	now := retentionNow
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = func() time.Time { return now }
	d.SetInteractiveRuntime(newPaneRuntime(false))

	d.SweepRetention(now)
	rec := seedParked(t, stateDir, "d-later", now.Add(-resumableRetention))
	now = now.Add(retentionSweepEvery - time.Second)
	d.SweepRetention(now)
	if got := mustLoad(t, stateDir, rec.ID); !parked(got) {
		t.Fatalf("swept again within %s: %s", retentionSweepEvery, got.Status)
	}
	now = now.Add(time.Second)
	d.SweepRetention(now)
	requireReleased(t, mustLoad(t, stateDir, rec.ID), ReleaseReasonRetention)
}
