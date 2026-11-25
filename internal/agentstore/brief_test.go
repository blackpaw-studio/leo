package agentstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/brief"
)

var briefAt = time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)

func sampleBrief() *brief.Brief {
	return &brief.Brief{Goal: "ship it", Status: "testing", Open: []string{"review"}, UpdatedAt: briefAt}
}

func TestSetBriefStoresAndClears(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := SetBrief(home, "a", sampleBrief()); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if !reflect.DeepEqual(recs["a"].Brief, sampleBrief()) {
		t.Fatalf("brief = %+v", recs["a"].Brief)
	}
	if err := SetBrief(home, "a", nil); err != nil {
		t.Fatal(err)
	}
	recs, _ = Load(FilePath(home))
	if recs["a"].Brief != nil {
		t.Fatalf("brief not cleared: %+v", recs["a"].Brief)
	}
}

func TestSetBriefRequiresRecord(t *testing.T) {
	if err := SetBrief(t.TempDir(), "ghost", sampleBrief()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetBrief on a missing record = %v, want ErrNotFound", err)
	}
}

// Respawn paths build a fresh Record and Save it; that must not wipe the
// agent's brief.
func TestSavePreservesBrief(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := SetBrief(home, "a", sampleBrief()); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, Record{Name: "a", SessionID: "sid", Brief: &brief.Brief{Goal: "other"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if !reflect.DeepEqual(recs["a"].Brief, sampleBrief()) {
		t.Fatalf("brief = %+v, want kept across Save", recs["a"].Brief)
	}
}

func TestPortableCarriesACopyOfTheBrief(t *testing.T) {
	src := Record{Name: "a", Brief: sampleBrief()}
	out := src.Portable()
	if !reflect.DeepEqual(out.Brief, src.Brief) {
		t.Fatalf("brief = %+v", out.Brief)
	}
	out.Brief.Open[0] = "changed"
	if src.Brief.Open[0] != "review" {
		t.Fatal("Portable shares the brief's slices with its source")
	}
}

func TestBriefOfReadsOneAgentsBrief(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "a"})
	_ = SetBrief(home, "a", sampleBrief())
	if got := BriefOf(home, "a"); !reflect.DeepEqual(got, sampleBrief()) {
		t.Fatalf("BriefOf = %+v", got)
	}
	if BriefOf(home, "ghost") != nil {
		t.Fatal("a missing agent has no brief")
	}
}

// A write that dies before the rename must leave agents.json as it was and
// no temp file behind.
func TestWriteIsAtomicWhenTheRenameFails(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", SessionID: "before"}); err != nil {
		t.Fatal(err)
	}
	path := FilePath(home)
	before, _ := os.ReadFile(path)

	orig := renameFile
	renameFile = func(string, string) error { return errors.New("disk gone") }
	defer func() { renameFile = orig }()

	if err := Save(home, Record{Name: "a", SessionID: "after"}); err == nil {
		t.Fatal("want the rename failure reported")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("agents.json changed by a failed write:\n%s", after)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}
