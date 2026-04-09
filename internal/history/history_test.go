package history

import (
	"testing"
)

func TestRecordAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Record("heartbeat", 0); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	entry := store.Get("heartbeat")
	if entry == nil {
		t.Fatal("Get() returned nil")
	}
	if entry.Task != "heartbeat" {
		t.Errorf("Task = %q, want heartbeat", entry.Task)
	}
	if entry.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", entry.ExitCode)
	}
	if entry.RunAt.IsZero() {
		t.Error("RunAt should not be zero")
	}
}

func TestRecordOverwrites(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Record("task1", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("task1", 1); err != nil {
		t.Fatal(err)
	}

	entry := store.Get("task1")
	if entry.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1 (latest)", entry.ExitCode)
	}
}

func TestGetMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if entry := store.Get("nonexistent"); entry != nil {
		t.Errorf("Get() = %v, want nil", entry)
	}
}

func TestAll(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Record("task1", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("task2", 1); err != nil {
		t.Fatal(err)
	}

	all := store.All()
	if len(all) != 2 {
		t.Errorf("All() returned %d entries, want 2", len(all))
	}
}
