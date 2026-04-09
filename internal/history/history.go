package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxHistoryPerTask = 10

// Entry records the result of a single task execution.
type Entry struct {
	Task     string    `json:"task"`
	ExitCode int       `json:"exit_code"`
	RunAt    time.Time `json:"run_at"`
}

// Store persists task execution history to a JSON file.
type Store struct {
	path string
}

// NewStore creates a history store at <workspace>/state/task-history.json.
func NewStore(workspace string) *Store {
	return &Store{
		path: filepath.Join(workspace, "state", "task-history.json"),
	}
}

// Record saves a task execution result, prepending to the list and trimming
// to maxHistoryPerTask entries.
func (s *Store) Record(task string, exitCode int) error {
	entries := s.load()

	entry := Entry{
		Task:     task,
		ExitCode: exitCode,
		RunAt:    time.Now(),
	}

	// Prepend new entry
	list := append([]Entry{entry}, entries[task]...)
	if len(list) > maxHistoryPerTask {
		list = list[:maxHistoryPerTask]
	}
	entries[task] = list

	return s.save(entries)
}

// Get returns the most recent execution entry for a task, or nil if not found.
func (s *Store) Get(task string) *Entry {
	entries := s.load()
	if list, ok := entries[task]; ok && len(list) > 0 {
		e := list[0]
		return &e
	}
	return nil
}

// GetAll returns all stored entries for a task (most recent first).
func (s *Store) GetAll(task string) []Entry {
	entries := s.load()
	return entries[task]
}

// All returns all task history entries.
func (s *Store) All() map[string][]Entry {
	return s.load()
}

func (s *Store) load() map[string][]Entry {
	entries := make(map[string][]Entry)
	data, err := os.ReadFile(s.path)
	if err != nil {
		return entries
	}

	// Try new format first: map[string][]Entry
	if err := json.Unmarshal(data, &entries); err == nil {
		return entries
	}

	// Fall back to old format: map[string]Entry (single entry per task)
	old := make(map[string]Entry)
	if err := json.Unmarshal(data, &old); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt task history at %s: %v\n", s.path, err)
		return make(map[string][]Entry)
	}

	// Migrate old format to new
	for task, e := range old {
		entries[task] = []Entry{e}
	}
	return entries
}

func (s *Store) save(entries map[string][]Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0750); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling history: %w", err)
	}

	// Atomic write via temp file + rename
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing history: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming history file: %w", err)
	}
	return nil
}
