// Package agentstore persists ephemeral agent records to disk.
// It is intentionally dependency-free (no imports from daemon, service, or web)
// to avoid import cycles.
package agentstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record persists an ephemeral agent so it can be restored after daemon restart.
type Record struct {
	Name       string            `json:"name"`
	Template   string            `json:"template"`
	Repo       string            `json:"repo,omitempty"`
	Workspace  string            `json:"workspace"`
	ClaudeArgs []string          `json:"claude_args"`
	Env        map[string]string `json:"env,omitempty"`
	WebPort    string            `json:"web_port"`
	SpawnedAt  time.Time         `json:"spawned_at"`
}

// FilePath returns the path to agents.json in the state directory.
func FilePath(homePath string) string {
	return filepath.Join(homePath, "state", "agents.json")
}

// Save persists an agent record to agents.json.
func Save(homePath string, record Record) error {
	path := FilePath(homePath)
	records, _ := Load(path)
	records[record.Name] = record
	return write(path, records)
}

// Remove deletes an agent record from agents.json.
func Remove(homePath, name string) {
	path := FilePath(homePath)
	records, _ := Load(path)
	delete(records, name)
	_ = write(path, records)
}

// Load reads all agent records from disk.
func Load(path string) (map[string]Record, error) {
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
