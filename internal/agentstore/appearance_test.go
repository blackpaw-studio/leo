package agentstore

import (
	"errors"
	"reflect"
	"testing"
)

func strPtr(s string) *string { return &s }

// A respawn path builds a fresh Record and Saves it; that must not wipe the
// agent's manual appearance.
func TestSavePreservesAppearance(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", Workspace: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetAppearance(home, "a", AppearancePatch{Emoji: strPtr("🏛️"), Color: strPtr("teal")}); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, Record{Name: "a", Workspace: "/w", SessionID: "sid"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, Record{Name: "a", Appearance: &Appearance{Color: "red"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	want := &Appearance{Emoji: "🏛️", Color: "teal"}
	if !reflect.DeepEqual(recs["a"].Appearance, want) {
		t.Fatalf("appearance = %+v, want %+v kept across Saves", recs["a"].Appearance, want)
	}
}

func TestSetAppearancePatch(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name  string
		patch AppearancePatch
		want  *Appearance
	}{
		{"set both", AppearancePatch{Emoji: strPtr("🎩"), Color: strPtr("pink")}, &Appearance{Emoji: "🎩", Color: "pink"}},
		{"absent leaves both", AppearancePatch{}, &Appearance{Emoji: "🎩", Color: "pink"}},
		{"set color only", AppearancePatch{Color: strPtr("teal")}, &Appearance{Emoji: "🎩", Color: "teal"}},
		{"clear emoji", AppearancePatch{Emoji: strPtr("")}, &Appearance{Color: "teal"}},
		{"clear color leaves nothing", AppearancePatch{Color: strPtr("")}, nil},
	}
	for _, step := range steps {
		got, err := SetAppearance(home, "a", step.patch)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !reflect.DeepEqual(got, step.want) {
			t.Fatalf("%s: returned %+v, want %+v", step.name, got, step.want)
		}
		recs, _ := Load(FilePath(home))
		if !reflect.DeepEqual(recs["a"].Appearance, step.want) {
			t.Fatalf("%s: stored %+v, want %+v", step.name, recs["a"].Appearance, step.want)
		}
	}
}

func TestSetAppearanceRequiresRecord(t *testing.T) {
	_, err := SetAppearance(t.TempDir(), "ghost", AppearancePatch{Color: strPtr("red")})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAppearance on a missing record = %v, want ErrNotFound", err)
	}
}

func TestRenameCarriesAppearance(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "old"})
	if _, err := SetAppearance(home, "old", AppearancePatch{Color: strPtr("teal")}); err != nil {
		t.Fatal(err)
	}
	if err := Rename(home, "old", "new", func(r Record) Record { r.Name = "new"; return r }); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if got := recs["new"].Appearance; got == nil || got.Color != "teal" {
		t.Fatalf("appearance after rename = %+v, want teal", got)
	}
}

func TestRemoveDropsAppearance(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, Record{Name: "a"})
	_, _ = SetAppearance(home, "a", AppearancePatch{Color: strPtr("teal")})
	Remove(home, "a")
	_ = Save(home, Record{Name: "a"})
	recs, _ := Load(FilePath(home))
	if recs["a"].Appearance != nil {
		t.Fatalf("a re-created record kept appearance %+v", recs["a"].Appearance)
	}
}

// A pending move's fence outlives any Save: only Update lifts it.
func TestSaveKeepsMovePending(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Record{Name: "a", Workspace: "/w"}); err != nil {
		t.Fatal(err)
	}
	pend := &MovePending{To: "laptop", RunID: "run-1"}
	if err := Update(home, "a", func(r Record) Record { r.MovePending = pend; return r }); err != nil {
		t.Fatal(err)
	}
	if err := Save(home, Record{Name: "a", Workspace: "/w", Stopped: true}); err != nil {
		t.Fatal(err)
	}
	recs, _ := Load(FilePath(home))
	if got := recs["a"].MovePending; got == nil || got.RunID != "run-1" {
		t.Fatalf("MovePending after Save = %+v", got)
	}
}
