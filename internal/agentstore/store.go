// Package agentstore persists ephemeral agent records to disk.
// It is intentionally dependency-free (no imports from daemon, service, or web)
// to avoid import cycles.
package agentstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// storeMu serializes Load/Save/Remove across goroutines so concurrent daemon
// handlers (spawn, stop, prune) don't perform interleaved read-modify-write
// cycles that clobber each other's changes to agents.json.
var storeMu sync.Mutex

// Record persists an ephemeral agent so it can be restored after daemon restart.
// Branch and CanonicalPath are set iff the agent was spawned with --worktree;
// when Branch is empty the agent uses the Workspace directly as claude's cwd.
//
// SessionID is the claude session ID captured at spawn time. On daemon restart
// RestoreAgents rewrites the agent's claude args to pass `--resume <SessionID>`
// so conversation context is preserved across restarts.
//
// Stopped is set by Manager.Stop for worktree agents — the record is kept so
// `leo agent prune` can find the checkout, but RestoreAgents skips records
// marked Stopped so a user-stopped agent is not resurrected on daemon restart.
// Shared-workspace agents delete the record on stop, so Stopped only applies
// to worktree agents in practice.
type Record struct {
	Name          string            `json:"name"`
	Template      string            `json:"template"`
	Repo          string            `json:"repo,omitempty"`
	Workspace     string            `json:"workspace"`
	Branch        string            `json:"branch,omitempty"`
	CanonicalPath string            `json:"canonical_path,omitempty"`
	ClaudeArgs    []string          `json:"claude_args"`
	SessionID     string            `json:"session_id,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	WebPort       string            `json:"web_port"`
	SpawnedAt     time.Time         `json:"spawned_at"`
	Stopped       bool              `json:"stopped,omitempty"`
}

// FilePath returns the path to agents.json in the state directory.
func FilePath(homePath string) string {
	return filepath.Join(homePath, "state", "agents.json")
}

// Save persists an agent record to agents.json.
func Save(homePath string, record Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	records[record.Name] = record
	return write(path, records)
}

// Remove deletes an agent record from agents.json.
func Remove(homePath, name string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	delete(records, name)
	_ = write(path, records)
}

// Load reads all agent records from disk.
func Load(path string) (map[string]Record, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	return loadLocked(path)
}

// loadLocked performs the read without acquiring storeMu. Callers that already
// hold the lock (Save, Remove) use this to avoid a re-entrant lock acquisition.
func loadLocked(path string) (map[string]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]Record), err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return make(map[string]Record), err
	}
	return records, nil
}

func write(path string, records map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent records: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
