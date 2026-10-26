package clienttools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// FlagState is what the daemon remembers across restarts about client-tool
// registrations: that an app has ever registered tools, and which names.
// The flag gates the fallback tools (leo_app_*); the names keep deny_tools
// entries valid at boot, before any app has reconnected.
type FlagState struct {
	EverRegistered bool     `json:"ever_registered"`
	Tools          []string `json:"tools"`
}

// FlagStore persists FlagState.
type FlagStore interface {
	// Load returns the stored state; a store with nothing returns the zero
	// state and no error.
	Load() (FlagState, error)
	Save(FlagState) error
}

// FileFlagStore keeps FlagState in a 0600 JSON file (<home>/state/client-tools.json).
type FileFlagStore struct{ path string }

// NewFileFlagStore returns a store over path.
func NewFileFlagStore(path string) *FileFlagStore { return &FileFlagStore{path: path} }

// Load reads the file; a missing file is the zero state.
func (s *FileFlagStore) Load() (FlagState, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return FlagState{}, nil
	}
	if err != nil {
		return FlagState{}, err
	}
	var st FlagState
	if err := json.Unmarshal(raw, &st); err != nil {
		return FlagState{}, fmt.Errorf("decoding %s: %w", s.path, err)
	}
	return st, nil
}

// Save writes the file atomically with mode 0600.
func (s *FileFlagStore) Save(st FlagState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(dir, s.path, raw)
}

// EverRegistered tracks, persistently, whether any app has registered client
// tools with this daemon and the names it has seen. A persistence failure is
// logged and otherwise ignored: the daemon keeps working from memory.
type EverRegistered struct {
	store FlagStore
	logf  func(format string, args ...any)

	mu    sync.Mutex
	state FlagState
	names map[string]struct{}
}

// NewEverRegistered loads the stored state from store. logf may be nil.
func NewEverRegistered(store FlagStore, logf func(format string, args ...any)) *EverRegistered {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	e := &EverRegistered{store: store, logf: logf, names: map[string]struct{}{}}
	st, err := store.Load()
	if err != nil {
		logf("client-tools: reading registration flag: %v", err)
	}
	e.state = FlagState{EverRegistered: st.EverRegistered}
	for _, n := range st.Tools {
		e.names[n] = struct{}{}
	}
	e.state.Tools = e.sortedLocked()
	return e
}

// Registered reports whether an app has ever registered client tools.
func (e *EverRegistered) Registered() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.EverRegistered
}

// Names returns every tool name ever registered, sorted.
func (e *EverRegistered) Names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sortedLocked()
}

// Record notes a registration of names, persisting only when it adds
// something.
func (e *EverRegistered) Record(names []string) {
	e.mu.Lock()
	changed := !e.state.EverRegistered
	for _, n := range names {
		if _, ok := e.names[n]; !ok {
			e.names[n] = struct{}{}
			changed = true
		}
	}
	if !changed {
		e.mu.Unlock()
		return
	}
	e.state = FlagState{EverRegistered: true, Tools: e.sortedLocked()}
	st := e.state
	e.mu.Unlock()

	if err := e.store.Save(st); err != nil {
		e.logf("client-tools: persisting registration flag: %v", err)
	}
}

func (e *EverRegistered) sortedLocked() []string {
	out := make([]string, 0, len(e.names))
	for n := range e.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// writeFileAtomic writes data to a temp file in dir and renames it over
// path with mode 0600.
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
	return nil
}
