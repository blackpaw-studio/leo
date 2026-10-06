package agentstore

import "testing"

// The opening's ack is recorded by the supervisor while other paths
// load-modify-Save the record: a Save holding a stale copy (Reset saves
// after respawning) must never undo or forge it.
func TestStaleSaveKeepsOpeningAck(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", SessionID: "sid"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := Load(FilePath(home))
	if err := SetOpeningAcked(home, "a", "open-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, stale["a"]); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if recs["a"].OpeningAckedID != "open-1" {
		t.Fatalf("OpeningAckedID = %q, want the setter's value to survive a stale Save", recs["a"].OpeningAckedID)
	}

	forged := recs["a"]
	forged.OpeningAckedID = "open-forged"
	if err := Save(home, forged); err != nil {
		t.Fatal(err)
	}
	recs, _ = Load(FilePath(home))
	if recs["a"].OpeningAckedID != "open-1" {
		t.Fatalf("Save set OpeningAckedID to %q", recs["a"].OpeningAckedID)
	}
	if err := SetOpeningAcked(home, "ghost", "open-1", ""); err == nil {
		t.Fatal("SetOpeningAcked on a missing record succeeded")
	}
}

// A bridged launch records the opening it queued and the launch it went to
// before the session starts, so a restarted daemon adopting that session
// can queue it again under the same id. Its ack clears it, but only the
// ack of that launch: a late ack of an earlier launch leaves a successor's
// queued opening alone.
func TestOpeningQueuedUntilItsLaunchAcks(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := Load(FilePath(home))
	if err := SetOpeningQueued(home, "a", "launch-2", "open-2"); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, stale["a"]); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if got := recs["a"]; got.OpeningQueuedLaunch != "launch-2" || got.OpeningQueuedID != "open-2" {
		t.Fatalf("queued opening = %q/%q, want it to survive a stale Save", got.OpeningQueuedLaunch, got.OpeningQueuedID)
	}

	if err := SetOpeningAcked(home, "a", "open-1", "launch-1"); err != nil {
		t.Fatal(err)
	}
	recs, _ = Load(FilePath(home))
	if got := recs["a"]; got.OpeningAckedID != "open-1" || got.OpeningQueuedLaunch != "launch-2" {
		t.Fatalf("an earlier launch's ack cleared the successor's queued opening: %+v", got)
	}

	if err := SetOpeningAcked(home, "a", "open-2", "launch-2"); err != nil {
		t.Fatal(err)
	}
	recs, _ = Load(FilePath(home))
	if got := recs["a"]; got.OpeningAckedID != "open-2" || got.OpeningQueuedLaunch != "" || got.OpeningQueuedID != "" {
		t.Fatalf("after its launch's ack: %+v, want it acked and no longer queued", got)
	}
	if err := SetOpeningQueued(home, "ghost", "launch-1", "open-1"); err == nil {
		t.Fatal("SetOpeningQueued on a missing record succeeded")
	}
}
