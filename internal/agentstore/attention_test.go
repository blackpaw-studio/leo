package agentstore

import (
	"sync"
	"testing"
)

// A load-modify-Save writer that read the record before the supervisor
// stored a launch's attention token must not wipe the token.
func TestStaleSaveKeepsAttentionToken(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", Workspace: "/w"}); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	stale := recs["a"]

	if err := SetAttentionToken(home, "a", "tok-1"); err != nil {
		t.Fatal(err)
	}
	stale.SessionID = "sid"
	if err := Save(home, stale); err != nil {
		t.Fatal(err)
	}

	recs, _ = Load(FilePath(home))
	if recs["a"].AttentionToken != "tok-1" || recs["a"].SessionID != "sid" {
		t.Fatalf("record = %+v, want token kept and session id saved", recs["a"])
	}
}

func TestSaveCannotSetAttentionToken(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", AttentionToken: "forged"}); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if recs["a"].AttentionToken != "" {
		t.Fatalf("token = %q, want Save to leave it alone", recs["a"].AttentionToken)
	}
}

func TestSetAttentionTokenClearsAndRequiresRecord(t *testing.T) {
	home := t.TempDir()
	if err := SetAttentionToken(home, "ghost", "tok"); err == nil {
		t.Fatal("SetAttentionToken on a missing record succeeded")
	}
	_ = Save(home, Record{Name: "a"})
	_ = SetAttentionToken(home, "a", "tok")
	if err := SetAttentionToken(home, "a", ""); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if recs["a"].AttentionToken != "" {
		t.Fatalf("token = %q, want cleared", recs["a"].AttentionToken)
	}
}

func TestRemoveThenSaveStartsWithoutToken(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "a"})
	_ = SetAttentionToken(home, "a", "tok")
	Remove(home, "a")
	_ = Save(home, Record{Name: "a"})
	recs, _ := Load(FilePath(home))
	if recs["a"].AttentionToken != "" {
		t.Fatalf("token = %q, want a re-created record to start clean", recs["a"].AttentionToken)
	}
}

func TestRenameCarriesAttentionToken(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "old"})
	_ = SetAttentionToken(home, "old", "tok")
	if err := Rename(home, "old", "new", func(r Record) Record { r.Name = "new"; return r }); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if recs["new"].AttentionToken != "tok" {
		t.Fatalf("token after rename = %q", recs["new"].AttentionToken)
	}
}

// Interleaves many stale whole-record Saves with token writes; the last
// token written must survive.
func TestConcurrentSavesNeverLoseAttentionToken(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "a"})
	recs, _ := Load(FilePath(home))
	stale := recs["a"]

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = Save(home, stale) }()
		go func() {
			defer wg.Done()
			if i == 0 {
				_ = SetAttentionToken(home, "a", "tok")
			}
		}()
	}
	wg.Wait()
	for range 5 {
		_ = Save(home, stale)
	}
	recs, _ = Load(FilePath(home))
	if recs["a"].AttentionToken != "tok" {
		t.Fatalf("token = %q, want tok", recs["a"].AttentionToken)
	}
}
