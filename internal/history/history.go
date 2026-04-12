package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxHistoryPerTask = 10

// Exit reasons for task execution.
const (
	ReasonSuccess = "success"
	ReasonFailure = "failure"
	ReasonTimeout = "timeout"
)

// Entry records the result of a single task execution.
type Entry struct {
	Task     string    `json:"task"`
	ExitCode int       `json:"exit_code"`
	Reason   string    `json:"reason,omitempty"`
	RunAt    time.Time `json:"run_at"`
	LogFile  string    `json:"log_file,omitempty"`
}

// Store persists task execution history to a JSON file.
type Store struct {
	path   string
	logDir string
}

// NewStore creates a history store at <workspace>/state/task-history.json.
// Log files recorded in history are stored as bare filenames inside
// <workspace>/state/logs, which the store uses when pruning.
func NewStore(workspace string) *Store {
	return &Store{
		path:   filepath.Join(workspace, "state", "task-history.json"),
		logDir: filepath.Join(workspace, "state", "logs"),
	}
}

// LogPath resolves a history entry's LogFile to an absolute path.
// Returns "" when the entry has no log file.
func (s *Store) LogPath(e Entry) string {
	if e.LogFile == "" {
		return ""
	}
	if filepath.IsAbs(e.LogFile) {
		return e.LogFile
	}
	return filepath.Join(s.logDir, e.LogFile)
}

// Record saves a task execution result, prepending to the list and trimming
// to maxHistoryPerTask entries. Old log files are deleted when entries are pruned.
func (s *Store) Record(task string, exitCode int, reason string, logFile string) error {
	entries := s.load()

	entry := Entry{
		Task:     task,
		ExitCode: exitCode,
		Reason:   reason,
		RunAt:    time.Now(),
		LogFile:  logFile,
	}

	// Prepend new entry
	list := append([]Entry{entry}, entries[task]...)
	if len(list) > maxHistoryPerTask {
		// Delete log files for pruned entries
		for _, pruned := range list[maxHistoryPerTask:] {
			if path := s.LogPath(pruned); path != "" {
				os.Remove(path)
			}
		}
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
