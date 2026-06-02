package agentstore

import (
	"path/filepath"
	"testing"
)

func writeRec(t *testing.T, home string, rec Record) {
	t.Helper()
	if err := Save(home, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestRename_ReKeysAndMutates(t *testing.T) {
	home := t.TempDir()
	writeRec(t, home, Record{Name: "leo-old", Template: "t", ClaudeArgs: []string{"--name", "leo-old", "--model", "opus"}})

	err := Rename(home, "leo-old", "leo-new", func(r Record) Record {
		r.Name = "leo-new"
		r.ClaudeArgs = []string{"--name", "leo-new", "--model", "opus"}
		return r
	})
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	recs, err := Load(filepath.Join(home, "state", "agents.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := recs["leo-old"]; ok {
		t.Fatal("old key still present")
	}
	got, ok := recs["leo-new"]
	if !ok {
		t.Fatal("new key missing")
	}
	if got.Name != "leo-new" || got.ClaudeArgs[1] != "leo-new" {
		t.Fatalf("mutate not applied: %+v", got)
	}
}

func TestRename_CollisionAndMissing(t *testing.T) {
	home := t.TempDir()
	writeRec(t, home, Record{Name: "leo-a"})
	writeRec(t, home, Record{Name: "leo-b"})

	if err := Rename(home, "leo-a", "leo-b", func(r Record) Record { return r }); err == nil {
		t.Fatal("expected collision error")
	}
	if err := Rename(home, "leo-missing", "leo-c", func(r Record) Record { return r }); err == nil {
		t.Fatal("expected missing-source error")
	}
}
