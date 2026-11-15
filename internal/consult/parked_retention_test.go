package consult

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecorderPruneKeepsParkedRecords(t *testing.T) {
	r, dir := newTestRecorder(t)
	parkedRec := Record{ID: "c-parked", Kind: "dispatch", Mode: ModeInteractive, Harness: "claude", Status: StatusClosed, Retained: true, SessionID: "sess-1", StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0)}
	if _, err := r.Open(parkedRec); err != nil {
		t.Fatal(err)
	}
	if err := r.PersistRecord(parkedRec); err != nil {
		t.Fatal(err)
	}
	for i := range RecordsKept + 2 {
		rec := testRecord(fmt.Sprintf("c-%04d", i))
		rec.StartedAt = rec.StartedAt.Add(time.Duration(i) * time.Minute)
		h, err := r.Open(rec)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := h.Close(StatusDone, nil); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
	if _, err := LoadOne(filepath.Dir(dir), parkedRec.ID); err != nil {
		t.Fatalf("the oldest record, a parked run, was pruned: %v", err)
	}
}

func TestRestartReconcileKeepsParkedWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	rec := seedIsolated(t, repo, stateDir, "d-rstpark", func(r *Record) {
		r.Status, r.Retained, r.WorktreeState, r.SessionID = StatusClosed, true, WorktreePresent, "sess-1"
	})
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	markInterruptedAndReconcile(t, d)

	got, err := LoadOne(stateDir, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(got) || got.WorktreeState != WorktreePresent || !dirExists(rec.Worktree) {
		t.Fatalf("restart touched a parked run: status=%s retained=%v worktree=%q dir=%v", got.Status, got.Retained, got.WorktreeState, dirExists(rec.Worktree))
	}
}

func TestPruneWorktreesSkipsParked(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	ended := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	rec := seedIsolated(t, repo, stateDir, "d-prunepark", func(r *Record) {
		r.Status, r.Retained, r.WorktreeState, r.SessionID, r.EndedAt = StatusClosed, true, WorktreePresent, "sess-1", ended
	})

	report, err := PruneWorktrees(stateDir, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemsByID(report)[rec.ID]
	until := ended.Add(resumableRetention).Format(time.RFC3339)
	if item.Action != PruneKeepRetained || !strings.Contains(item.Reason, "retained for follow-up until "+until) {
		t.Fatalf("item = %+v", item)
	}
	if !dirExists(rec.Worktree) {
		t.Fatal("prune removed a parked run's worktree")
	}
}

func TestMarkInterruptedLeavesParkedRun(t *testing.T) {
	stateDir := t.TempDir()
	ended := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: "d-mipark", Kind: "dispatch", Mode: ModeInteractive, Harness: "claude", Status: StatusClosed, Retained: true, SessionID: "sess-1", Cwd: t.TempDir(), StartedAt: ended.Add(-time.Hour), EndedAt: ended,
		Turns: []Turn{{TurnID: "d-mipark#1", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "answer", ResultCollectedAt: ended}}}
	recorder := NewFileRecorder(stateDir)
	if _, err := recorder.Open(rec); err != nil {
		t.Fatal(err)
	}
	if err := recorder.PersistRecord(rec); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	markInterruptedAndReconcile(t, d)

	got, err := LoadOne(stateDir, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(got) || got.Error != "" || !got.EndedAt.Equal(ended) || len(got.Notifications) != 0 {
		t.Fatalf("restart changed a parked run: %+v", got)
	}
}
