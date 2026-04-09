package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestRecordKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Record("task1", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("task1", 1); err != nil {
		t.Fatal(err)
	}

	// Get returns most recent
	entry := store.Get("task1")
	if entry.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1 (latest)", entry.ExitCode)
	}

	// GetAll returns both entries, most recent first
	all := store.GetAll("task1")
	if len(all) != 2 {
		t.Fatalf("GetAll() returned %d entries, want 2", len(all))
	}
	if all[0].ExitCode != 1 {
		t.Errorf("all[0].ExitCode = %d, want 1", all[0].ExitCode)
	}
	if all[1].ExitCode != 0 {
		t.Errorf("all[1].ExitCode = %d, want 0", all[1].ExitCode)
	}
}

func TestRecordTrimsToMax(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	for i := 0; i < 15; i++ {
		if err := store.Record("task1", i); err != nil {
			t.Fatal(err)
		}
	}

	all := store.GetAll("task1")
	if len(all) != maxHistoryPerTask {
		t.Errorf("GetAll() returned %d entries, want %d", len(all), maxHistoryPerTask)
	}

	// Most recent should be exit code 14 (last recorded)
	if all[0].ExitCode != 14 {
		t.Errorf("all[0].ExitCode = %d, want 14", all[0].ExitCode)
	}

	// Oldest kept should be exit code 5 (15 - 10)
	if all[maxHistoryPerTask-1].ExitCode != 5 {
		t.Errorf("all[%d].ExitCode = %d, want 5", maxHistoryPerTask-1, all[maxHistoryPerTask-1].ExitCode)
	}
}

func TestGetMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if entry := store.Get("nonexistent"); entry != nil {
		t.Errorf("Get() = %v, want nil", entry)
	}
}

func TestGetAllMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	entries := store.GetAll("nonexistent")
	if entries != nil {
		t.Errorf("GetAll() = %v, want nil", entries)
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
		t.Errorf("All() returned %d tasks, want 2", len(all))
	}
	if len(all["task1"]) != 1 {
		t.Errorf("task1 has %d entries, want 1", len(all["task1"]))
	}
	if len(all["task2"]) != 1 {
		t.Errorf("task2 has %d entries, want 1", len(all["task2"]))
	}
}

func TestMigrateOldFormat(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Write old format: map[string]Entry
	oldData := map[string]Entry{
		"heartbeat": {
			Task:     "heartbeat",
			ExitCode: 0,
			RunAt:    time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	data, err := json.Marshal(oldData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "task-history.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)

	// Get should still work
	entry := store.Get("heartbeat")
	if entry == nil {
		t.Fatal("Get() returned nil after migration")
	}
	if entry.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", entry.ExitCode)
	}

	// GetAll should return the migrated entry
	all := store.GetAll("heartbeat")
	if len(all) != 1 {
		t.Fatalf("GetAll() returned %d entries, want 1", len(all))
	}

	// Recording should work on top of migrated data
	if err := store.Record("heartbeat", 1); err != nil {
		t.Fatal(err)
	}
	all = store.GetAll("heartbeat")
	if len(all) != 2 {
		t.Fatalf("GetAll() returned %d entries after record, want 2", len(all))
	}
	if all[0].ExitCode != 1 {
		t.Errorf("all[0].ExitCode = %d, want 1", all[0].ExitCode)
	}
}
