package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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

// Record saves a task execution result.
func (s *Store) Record(task string, exitCode int) error {
	entries := s.load()

	entries[task] = Entry{
		Task:     task,
		ExitCode: exitCode,
		RunAt:    time.Now(),
	}

	return s.save(entries)
}

// Get returns the last execution entry for a task, or nil if not found.
func (s *Store) Get(task string) *Entry {
	entries := s.load()
	if e, ok := entries[task]; ok {
		return &e
	}
	return nil
}

// All returns all task history entries.
func (s *Store) All() map[string]Entry {
	return s.load()
}

func (s *Store) load() map[string]Entry {
	entries := make(map[string]Entry)
	data, err := os.ReadFile(s.path)
	if err != nil {
		return entries
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt task history at %s: %v\n", s.path, err)
		return make(map[string]Entry)
	}
	return entries
}

func (s *Store) save(entries map[string]Entry) error {
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
