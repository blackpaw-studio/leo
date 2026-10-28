package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	followCacheFile  = "follow-cache.json"
	followStatusFile = "follow-status.json"
	// followCacheSchema versions the cache file itself (not the export).
	followCacheSchema = 1
	// followStaleFactor: a cache older than this many intervals is stale.
	followStaleFactor = 3
)

// FollowCache is the last successful pull, kept in
// <state>/follow-cache.json (0600: environments can hold tokens). Export is
// the lead's export verbatim; Load layers it under local config.
type FollowCache struct {
	Schema       int             `json:"schema"`
	Host         string          `json:"host"`
	SSH          string          `json:"ssh"`
	PulledAt     time.Time       `json:"pulled_at"`
	LeadRevision string          `json:"lead_revision"`
	LeadVersion  string          `json:"lead_version"`
	LeadHomeID   string          `json:"lead_home_id"`
	Digest       string          `json:"digest"`
	Export       json.RawMessage `json:"export"`
}

// Stale reports whether the cache is older than 3 × interval at now.
// Stale is shown, never enforced.
func (fc *FollowCache) Stale(now time.Time, interval time.Duration) bool {
	return now.Sub(fc.PulledAt) > followStaleFactor*interval
}

// FollowStatus records the latest pull attempt, in <state>/follow-status.json.
// It is written on every attempt, so a failed pull never touches the cache.
type FollowStatus struct {
	LastAttempt   time.Time       `json:"last_attempt"`
	LastError     string          `json:"last_error,omitempty"`
	LastErrorCode FollowErrorCode `json:"last_error_code,omitempty"`
}

// ReadFollowCache returns the cache under statePath, nil when there is none.
func ReadFollowCache(statePath string) (*FollowCache, error) {
	var fc FollowCache
	ok, err := readStateJSON(filepath.Join(statePath, followCacheFile), &fc)
	if err != nil || !ok {
		return nil, err
	}
	return &fc, nil
}

// WriteFollowCache atomically replaces the cache (mode 0600).
func WriteFollowCache(statePath string, fc FollowCache) error {
	fc.Schema = followCacheSchema
	return writeStateJSON(statePath, followCacheFile, fc)
}

// ReadFollowStatus returns the last attempt's status, zero when none.
func ReadFollowStatus(statePath string) (FollowStatus, error) {
	var st FollowStatus
	_, err := readStateJSON(filepath.Join(statePath, followStatusFile), &st)
	return st, err
}

// WriteFollowStatus atomically replaces the status file (mode 0600).
func WriteFollowStatus(statePath string, st FollowStatus) error {
	return writeStateJSON(statePath, followStatusFile, st)
}

func readStateJSON(path string, out any) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // under the leo state dir
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func writeStateJSON(statePath, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	path := filepath.Join(statePath, name)
	if err := writeFileAtomicWith(path, append(data, '\n'), false); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
