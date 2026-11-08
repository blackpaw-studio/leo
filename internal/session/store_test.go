package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreGetSetDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Get on empty store returns not found
	sid, found, err := store.Get("task:heartbeat")
	if err != nil {
		t.Fatalf("Get on empty store: %v", err)
	}
	if found {
		t.Fatal("expected not found on empty store")
	}
	if sid != "" {
		t.Fatalf("expected empty session ID, got %q", sid)
	}

	// Set and Get
	if err := store.Set("task:heartbeat", "session-abc"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	sid, found, err = store.Get("task:heartbeat")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !found {
		t.Fatal("expected found after Set")
	}
	if sid != "session-abc" {
		t.Fatalf("expected session-abc, got %q", sid)
	}

	// Set a second key
	if err := store.Set("task:daily", "session-def"); err != nil {
		t.Fatalf("Set second key: %v", err)
	}

	// Both keys should exist
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Delete one key
	if err := store.Delete("task:heartbeat"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, found, err = store.Get("task:heartbeat")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if found {
		t.Fatal("expected not found after Delete")
	}

	// Other key still exists
	sid, found, err = store.Get("task:daily")
	if err != nil {
		t.Fatalf("Get other key after Delete: %v", err)
	}
	if !found || sid != "session-def" {
		t.Fatalf("expected session-def, got %q (found=%v)", sid, found)
	}
}

func TestStoreDeleteAll(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Set("task:a", "s1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set("task:b", "s2"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List after DeleteAll: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after DeleteAll, got %d", len(entries))
	}
}

func TestStoreOverwrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Set("task:x", "old-session"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set("task:x", "new-session"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	sid, found, err := store.Get("task:x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || sid != "new-session" {
		t.Fatalf("expected new-session, got %q", sid)
	}
}

func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Set("key", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify no temp file left behind
	tmpPath := filepath.Join(dir, "state", "sessions.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should not exist after successful write")
	}

	// Verify the file exists
	mainPath := filepath.Join(dir, "state", "sessions.json")
	if _, err := os.Stat(mainPath); err != nil {
		t.Fatalf("sessions.json should exist: %v", err)
	}
}

func TestStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sessions.json"), []byte("not json"), 0600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	store := NewStore(dir)
	_, _, err := store.Get("key")
	if err == nil {
		t.Fatal("expected error on corrupt file")
	}
}

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()

	if id1 == id2 {
		t.Fatal("NewID should generate unique IDs")
	}
	if len(id1) != 36 {
		t.Fatalf("expected UUID length 36, got %d: %q", len(id1), id1)
	}
}

func TestStoreLookupReadsRunCount(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.SetRuns("task:a", "sess-1", 4); err != nil {
		t.Fatalf("SetRuns: %v", err)
	}
	entry, found, err := store.Lookup("task:a")
	if err != nil || !found {
		t.Fatalf("Lookup = found %v, err %v", found, err)
	}
	if entry.SessionID != "sess-1" || entry.Runs != 4 {
		t.Errorf("entry = %+v, want sess-1 with 4 runs", entry)
	}

	// Plain Set stores no count.
	if err := store.Set("task:a", "sess-2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entry, _, _ = store.Lookup("task:a")
	if entry.Runs != 0 {
		t.Errorf("Runs after Set = %d, want 0", entry.Runs)
	}

	if _, found, err := store.Lookup("task:missing"); err != nil || found {
		t.Errorf("Lookup(missing) = found %v, err %v", found, err)
	}
}

func TestStoreLegacyEntryWithoutRunsReadsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"task:old":{"session_id":"legacy-id","updated_at":"2026-01-02T03:04:05Z"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	entry, found, err := NewStore(dir).Lookup("task:old")
	if err != nil || !found {
		t.Fatalf("Lookup = found %v, err %v", found, err)
	}
	if entry.SessionID != "legacy-id" || entry.Runs != 0 {
		t.Errorf("entry = %+v, want legacy-id with 0 runs", entry)
	}
}
