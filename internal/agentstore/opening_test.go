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
	if err := SetOpeningAcked(home, "a", "open-1"); err != nil {
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

	if err := SetOpeningAcked(home, "a", ""); err != nil {
		t.Fatal(err)
	}
	recs, _ = Load(FilePath(home))
	if recs["a"].OpeningAckedID != "" {
		t.Fatalf("clearing left %q", recs["a"].OpeningAckedID)
	}
	if err := SetOpeningAcked(home, "ghost", "open-1"); err == nil {
		t.Fatal("SetOpeningAcked on a missing record succeeded")
	}
}
