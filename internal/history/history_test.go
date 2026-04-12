package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Record("heartbeat", 0, ReasonSuccess, ""); err != nil {
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

	if err := store.Record("task1", 0, ReasonSuccess, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("task1", 1, ReasonFailure, ""); err != nil {
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
		if err := store.Record("task1", i, ReasonSuccess, ""); err != nil {
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

	if err := store.Record("task1", 0, ReasonSuccess, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("task2", 1, ReasonFailure, ""); err != nil {
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
	if err := store.Record("heartbeat", 1, ReasonFailure, ""); err != nil {
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

func TestLogPathResolution(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cases := []struct {
		name    string
		logFile string
		wantRel bool
		want    string
	}{
		{"empty", "", false, ""},
		{"filename", "task-20260101-000000.000.log", true, "task-20260101-000000.000.log"},
		{"absolute", "/var/log/already-absolute.log", false, "/var/log/already-absolute.log"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.LogPath(Entry{LogFile: tc.logFile})
			if tc.wantRel {
				want := filepath.Join(dir, "state", "logs", tc.want)
				if got != want {
					t.Errorf("LogPath(%q) = %q, want %q", tc.logFile, got, want)
				}
				return
			}
			if got != tc.want {
				t.Errorf("LogPath(%q) = %q, want %q", tc.logFile, got, tc.want)
			}
		})
	}
}

func TestPruneRemovesLogFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	logDir := filepath.Join(dir, "state", "logs")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create 11 entries with log files; the oldest should be pruned.
	var oldestLog string
	for i := 0; i < 11; i++ {
		logName := fmt.Sprintf("task-%02d.log", i)
		logPath := filepath.Join(logDir, logName)
		if err := os.WriteFile(logPath, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldestLog = logPath
		}
		if err := store.Record("task1", 0, ReasonSuccess, logName); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(oldestLog); !os.IsNotExist(err) {
		t.Errorf("expected oldest log file pruned, stat err = %v", err)
	}
}
