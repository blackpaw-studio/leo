package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry represents a stored session mapping.
type Entry struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
	// Runs counts completed runs against SessionID, for tasks that rotate
	// their session. Entries written before the field existed read as 0.
	Runs int `json:"runs,omitempty"`
}

// Store manages session ID persistence in a JSON file.
type Store struct {
	path string
}

// NewStore creates a Store backed by <workspace>/state/sessions.json.
func NewStore(workspace string) *Store {
	return &Store{
		path: filepath.Join(workspace, "state", "sessions.json"),
	}
}

// Get returns the session ID for the given key, or empty string if not found.
func (s *Store) Get(key string) (string, bool, error) {
	entries, err := s.load()
	if err != nil {
		return "", false, err
	}
	entry, ok := entries[key]
	if !ok {
		return "", false, nil
	}
	return entry.SessionID, true, nil
}

// Lookup returns the full stored entry (session ID and run count) for the
// given key.
func (s *Store) Lookup(key string) (Entry, bool, error) {
	entries, err := s.load()
	if err != nil {
		return Entry{}, false, err
	}
	entry, ok := entries[key]
	return entry, ok, nil
}

// Set stores a session ID for the given key, with no run count.
func (s *Store) Set(key string, sessionID string) error {
	return s.SetRuns(key, sessionID, 0)
}

// SetRuns stores a session ID together with the number of runs completed
// against it.
func (s *Store) SetRuns(key string, sessionID string, runs int) error {
	entries, err := s.load()
	if err != nil {
		return err
	}
	entries[key] = Entry{
		SessionID: sessionID,
		UpdatedAt: time.Now().UTC(),
		Runs:      runs,
	}
	return s.save(entries)
}

// Delete removes a session mapping by key.
func (s *Store) Delete(key string) error {
	entries, err := s.load()
	if err != nil {
		return err
	}
	delete(entries, key)
	return s.save(entries)
}

// DeleteAll removes all stored sessions.
func (s *Store) DeleteAll() error {
	return s.save(make(map[string]Entry))
}

// List returns all stored session entries.
func (s *Store) List() (map[string]Entry, error) {
	return s.load()
}

func (s *Store) load() (map[string]Entry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]Entry), nil
		}
		return nil, fmt.Errorf("reading sessions: %w", err)
	}

	var entries map[string]Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing sessions: %w", err)
	}
	if entries == nil {
		entries = make(map[string]Entry)
	}
	return entries, nil
}

func (s *Store) save(entries map[string]Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0750); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling sessions: %w", err)
	}

	// Atomic write via temp file + rename
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing sessions: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming sessions file: %w", err)
	}
	return nil
}
