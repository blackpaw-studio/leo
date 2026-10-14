package drain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PendingResumeVersion is the on-disk format version of PendingResume.
const PendingResumeVersion = 1

const pendingResumeFile = "pending-resume.json"

// PendingResume is the drain set a committed drain hands to the next daemon.
type PendingResume struct {
	Version int      `json:"version"`
	DrainID string   `json:"drain_id"`
	Agents  []string `json:"agents"`
	// DeferredTasks are the cron tasks whose firings the drain held.
	DeferredTasks []string  `json:"deferred_tasks,omitempty"`
	WrittenAt     time.Time `json:"written_at"`
}

// storeMu serializes access to pending-resume.json within the process, as
// agentstore.storeMu does for agents.json.
var storeMu sync.Mutex

// FileStore keeps PendingResume at <state>/pending-resume.json.
type FileStore struct {
	path string
}

// NewFileStore returns the store under statePath.
func NewFileStore(statePath string) *FileStore {
	return &FileStore{path: filepath.Join(statePath, pendingResumeFile)}
}

// Write replaces the file atomically with mode 0600.
func (s *FileStore) Write(p PendingResume) error {
	p.Version = PendingResumeVersion
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pending resume: %w", err)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if err := writeFileAtomic(dir, s.path, data); err != nil {
		return fmt.Errorf("writing %s: %w", pendingResumeFile, err)
	}
	return nil
}

// Remove deletes the file; a missing file is not an error.
func (s *FileStore) Remove() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", pendingResumeFile, err)
	}
	return nil
}

// Read returns the file's contents without consuming it: the file stays
// the resume obligation until Resume has handled every agent. ok is false
// when there is none. A file that does not decode is renamed to
// pending-resume.json.corrupt-<unix> so it is reported once and never
// retried.
func (s *FileStore) Read() (PendingResume, bool, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return PendingResume{}, false, nil
	}
	if err != nil {
		return PendingResume{}, false, fmt.Errorf("reading %s: %w", pendingResumeFile, err)
	}
	var p PendingResume
	decodeErr := json.Unmarshal(data, &p)
	if decodeErr == nil && p.Version != PendingResumeVersion {
		decodeErr = fmt.Errorf("unsupported version %d", p.Version)
	}
	if decodeErr != nil {
		quarantine := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if err := os.Rename(s.path, quarantine); err != nil {
			return PendingResume{}, false, fmt.Errorf("decoding %s: %w (quarantine failed: %v)", pendingResumeFile, decodeErr, err)
		}
		return PendingResume{}, false, fmt.Errorf("decoding %s (moved to %s): %w", pendingResumeFile, filepath.Base(quarantine), decodeErr)
	}
	return p, true, nil
}

// writeFileAtomic writes data to a temp file in dir and renames it over
// path, then enforces 0600 explicitly (a create-time mode alone would not
// tighten a file another path wrote more loosely).
func writeFileAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the store's own directory
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
