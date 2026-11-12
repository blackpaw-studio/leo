package codex

import (
	"path/filepath"
	"testing"
	"time"
)

// dateDir is codex's sessions/YYYY/MM/DD directory for t.
func dateDir(home string, t time.Time) string {
	return filepath.Join(home, "sessions", t.Format("2006"), t.Format("01"), t.Format("02"))
}

const testRolloutID = "11111111-2222-4333-8444-555555555555"

func TestRolloutPath(t *testing.T) {
	home := t.TempDir()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	want := filepath.Join(dateDir(home, at), "rollout-2026-11-12T10-00-00-"+testRolloutID+".jsonl")
	writeRollout(t, want, testRolloutID, t.TempDir(), at)

	if got := RolloutPath(home, testRolloutID); got != want {
		t.Fatalf("RolloutPath = %q, want %q", got, want)
	}
	if got := RolloutPath(home, "missing"); got != "" {
		t.Fatalf("RolloutPath(missing) = %q, want empty", got)
	}
	if got := RolloutPath(home, ""); got != "" {
		t.Fatalf("RolloutPath(empty id) = %q, want empty", got)
	}
}

func TestLatestRolloutFor_DateDirsOnly(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	since := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)

	// A rollout in a date dir older than since-1day, with a fresh mtime and
	// the right cwd: a resumed old session. The fallback must not look there.
	oldDir := dateDir(home, since.AddDate(0, 0, -5))
	writeRollout(t, filepath.Join(oldDir, "rollout-old-uuid-old.jsonl"), "uuid-old", ws, since.Add(time.Minute))
	// Yesterday's dir is in range (a session started before midnight).
	yDir := dateDir(home, since.AddDate(0, 0, -1))
	writeRollout(t, filepath.Join(yDir, "rollout-y-uuid-y.jsonl"), "uuid-y", ws, since.Add(2*time.Minute))
	// Other cwd, and one older than since: neither matches.
	writeRollout(t, filepath.Join(dateDir(home, since), "rollout-o-uuid-other.jsonl"), "uuid-other", t.TempDir(), since.Add(time.Minute))
	writeRollout(t, filepath.Join(dateDir(home, since), "rollout-s-uuid-stale.jsonl"), "uuid-stale", ws, since.Add(-time.Minute))

	path, id, n := LatestRolloutFor(home, ws, since)
	if id != "uuid-y" || n != 1 || filepath.Base(path) != "rollout-y-uuid-y.jsonl" {
		t.Fatalf("LatestRolloutFor = %q %q %d, want uuid-y once", path, id, n)
	}

	writeRollout(t, filepath.Join(dateDir(home, since), "rollout-n-uuid-new.jsonl"), "uuid-new", ws, since.Add(3*time.Minute))
	path, id, n = LatestRolloutFor(home, ws, since)
	if id != "uuid-new" || n != 2 || path == "" {
		t.Fatalf("two matches = %q %q %d, want newest uuid-new with n=2", path, id, n)
	}

	if _, id, n = LatestRolloutFor(t.TempDir(), ws, since); id != "" || n != 0 {
		t.Fatalf("no sessions dir = %q %d", id, n)
	}
}
