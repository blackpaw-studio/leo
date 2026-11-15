package consult

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parkIsolated starts an isolated dispatch and closes it on its delivered
// result, leaving it parked with its worktree present.
func parkIsolated(t *testing.T, stateDir string) (*Dispatcher, *paneRuntime, string) {
	t.Helper()
	d, rt, _, id := startParkable(t, stateDir, true, "the answer")
	d.SweepNotifications(context.Background())
	requireParked(t, d, id, id+"#1")
	return d, rt, id
}

func requireReleased(t *testing.T, rec Record, reason string) {
	t.Helper()
	if rec.Status != StatusReleased || rec.Retained || rec.ReleaseReason != reason {
		t.Fatalf("status=%s retained=%v reason=%q, want released/%s", rec.Status, rec.Retained, rec.ReleaseReason, reason)
	}
}

func TestReleaseParkedRemovesCleanWorktree(t *testing.T) {
	stateDir := t.TempDir()
	d, _, id := parkIsolated(t, stateDir)
	parkedRec, _ := d.Get(id)

	rec, err := d.Release(id)
	if err != nil {
		t.Fatal(err)
	}
	requireReleased(t, rec, ReleaseReasonManual)
	if rec.WorktreeState != WorktreeRemoved || dirExists(parkedRec.Worktree) {
		t.Fatalf("worktree state=%q exists=%v", rec.WorktreeState, dirExists(parkedRec.Worktree))
	}
	disk, err := LoadOne(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	requireReleased(t, disk, ReleaseReasonManual)
}

func TestReleaseParkedKeepsDirtyWorktree(t *testing.T) {
	stateDir := t.TempDir()
	d, _, id := parkIsolated(t, stateDir)
	parkedRec, _ := d.Get(id)
	work := filepath.Join(parkedRec.Worktree, "uncommitted.txt")
	if err := os.WriteFile(work, []byte("work in progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec, err := d.Release(id)
	if err != nil {
		t.Fatal(err)
	}
	requireReleased(t, rec, ReleaseReasonManual)
	if rec.WorktreeState != WorktreeKept {
		t.Fatalf("worktree state = %q, want kept", rec.WorktreeState)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "work in progress\n" {
		t.Fatalf("uncommitted work lost: %q, %v", data, err)
	}
}

func TestReleaseParkedEvictedRecord(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	seeded := seedIsolated(t, repo, stateDir, "d-evicted", func(r *Record) {
		r.Status, r.Retained, r.WorktreeState, r.SessionID = StatusClosed, true, WorktreePresent, "sess-1"
	})
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	rec, err := d.Release(seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireReleased(t, rec, ReleaseReasonManual)
	if rec.WorktreeState != WorktreeRemoved || dirExists(seeded.Worktree) {
		t.Fatalf("worktree state=%q exists=%v", rec.WorktreeState, dirExists(seeded.Worktree))
	}
	disk, err := LoadOne(stateDir, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireReleased(t, disk, ReleaseReasonManual)
}

func TestSendAfterReleaseRejected(t *testing.T) {
	t.Run("manual release of a parked run", func(t *testing.T) {
		d, rt, _, id := startParkable(t, t.TempDir(), false, "the answer")
		d.SweepNotifications(context.Background())
		if _, err := d.Release(id); err != nil {
			t.Fatal(err)
		}
		_, err := d.SendWithConfig(context.Background(), testConfig(), id, "one more thing")
		want := "dispatch " + id + " was released (leo_release); start a new dispatch"
		if err == nil || err.Error() != want {
			t.Fatalf("send error = %v, want %q", err, want)
		}
		if rt.injectionCount() != 0 {
			t.Fatal("a message was injected into a released run")
		}
	})
	for _, c := range []struct {
		name string
		rec  Record
		want string
	}{
		{"retention release", Record{ReleaseReason: ReleaseReasonRetention}, "was released after 7 days without a follow-up (retention); start a new dispatch"},
		{"legacy release on finish", Record{ReleaseOnFinish: true, ReleasedOnFinish: true}, "was released after finishing (release_on_finish); start a new dispatch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stateDir := t.TempDir()
			recorder := NewFileRecorder(stateDir)
			rec := c.rec
			rec.ID, rec.Kind, rec.Mode, rec.Harness, rec.Status = "d-rel", "dispatch", ModeInteractive, "claude", StatusReleased
			if _, err := recorder.Open(rec); err != nil {
				t.Fatal(err)
			}
			if err := recorder.PersistRecord(rec); err != nil {
				t.Fatal(err)
			}
			d := NewDispatcher(NewFileRecorder(stateDir))
			_, err := d.SendWithConfig(context.Background(), testConfig(), "d-rel", "more")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCloseFinishedSkipsParkedRuns(t *testing.T) {
	d, _, _, id := startParkable(t, t.TempDir(), false, "the answer")
	d.SweepNotifications(context.Background())

	closed, err := d.CloseFinished("$1")
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatalf("close-finished released a parked run: %+v", closed)
	}
	requireParked(t, d, id, id+"#1")
}
