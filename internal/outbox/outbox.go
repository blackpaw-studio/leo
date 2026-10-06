// Package outbox persists, per agent, the messages leo has accepted for the
// agent's claude and not yet seen it take: each leo bridge deliver, from the
// moment it is queued until the mod acks it (ok or not). The entries outlive
// the claude launch and the daemon that queued them, so the agent's next
// launch carries them over. An agent's entries live in one JSON file under
// the store's directory, named for the agent and rewritten whole on every
// change (write to a temp file, fsync, rename). It is dependency-free.
package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxEntries caps one agent's queued messages. The daemon sets
	// it to the bridge hub's per-agent cap, so a launch can carry a full
	// outbox over at once.
	DefaultMaxEntries = 256
	// DefaultMaxBytes caps the text one agent has queued: a few messages at
	// the web server's 10 MiB request cap, and far more of a usual size.
	DefaultMaxBytes = 32 << 20
)

var (
	// ErrFull: the agent has as many messages queued as the store holds.
	// The sender is told; nothing queued is ever dropped to make room.
	ErrFull = errors.New("agent outbox full")
	// ErrDuplicate: an entry under that id is queued already.
	ErrDuplicate = errors.New("message already queued")
	// ErrInvalidAgent: the name cannot be a file name in the store.
	ErrInvalidAgent = errors.New("invalid outbox agent name")
	// ErrNameHasMessages: a rename's new name has messages queued already.
	ErrNameHasMessages = errors.New("the new name has undelivered messages queued")
)

// Entry is one queued message: a bridge deliver and who sent it.
type Entry struct {
	// ID is the deliver's bridge command id; it is redelivered under it, so
	// the mod's dedup turns a repeat into a re-ack.
	ID     string `json:"id"`
	Text   string `json:"text"`
	AsUser bool   `json:"as_user,omitempty"`
	// From is the agent that sent it, told should it never be delivered;
	// "" for a human or a task.
	From     string    `json:"from,omitempty"`
	QueuedAt time.Time `json:"queued_at"`
}

// Options configures a Store. Zero values select the defaults.
type Options struct {
	MaxEntries int
	MaxBytes   int64
	// Logf reports what the store does on its own: moving a corrupt file
	// aside. nil writes to stderr.
	Logf func(format string, args ...any)
}

// Store holds every agent's queued messages under one directory. It is
// safe for concurrent use; each method is atomic on its own.
type Store struct {
	dir        string
	maxEntries int
	maxBytes   int64
	logf       func(format string, args ...any)
	mu         sync.Mutex
}

// file is an agent's file as stored.
type file struct {
	Entries []Entry `json:"entries"`
}

// New returns the store kept in dir, created on first write. It sweeps
// the temp files of writes that died before their rename (a crashed
// daemon's): nothing else writes in dir, and no write of this store has
// begun yet.
func New(dir string, opts Options) *Store {
	s := &Store{dir: dir, maxEntries: opts.MaxEntries, maxBytes: opts.MaxBytes, logf: opts.Logf}
	if s.maxEntries <= 0 {
		s.maxEntries = DefaultMaxEntries
	}
	if s.maxBytes <= 0 {
		s.maxBytes = DefaultMaxBytes
	}
	if s.logf == nil {
		s.logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	s.sweepStrayWrites()
	return s
}

// tempPattern matches the temp files writeFileAtomic creates in the dir.
const tempPattern = ".*.json.*.tmp"

func (s *Store) sweepStrayWrites() {
	strays, err := filepath.Glob(filepath.Join(s.dir, tempPattern))
	if err != nil {
		return
	}
	for _, path := range strays {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logf("outbox: removing the stray write %s: %v", path, err)
		}
	}
}

// Append queues e last for agent. It fails with ErrFull rather than exceed
// the caps, and with ErrDuplicate for an id already queued.
func (s *Store) Append(agent string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked(agent)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(entries, func(q Entry) bool { return q.ID == e.ID }) {
		return fmt.Errorf("%w: agent %s id %s", ErrDuplicate, agent, e.ID)
	}
	if len(entries) >= s.maxEntries {
		return fmt.Errorf("%w: agent %s has %d messages queued", ErrFull, agent, len(entries))
	}
	if size := textBytes(entries) + int64(len(e.Text)); size > s.maxBytes {
		return fmt.Errorf("%w: agent %s would have %d bytes queued, over %d", ErrFull, agent, size, s.maxBytes)
	}
	return s.writeLocked(agent, append(entries, e))
}

// Remove forgets agent's entry id; one not queued is not an error.
func (s *Store) Remove(agent, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked(agent)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(entries), func(e Entry) bool { return e.ID == id })
	if len(kept) == len(entries) {
		return nil
	}
	return s.writeLocked(agent, kept)
}

// List returns agent's entries in the order they were queued.
func (s *Store) List(agent string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(agent)
}

// Drop forgets everything agent had queued and returns it.
func (s *Store) Drop(agent string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked(agent)
	if err != nil {
		return nil, err
	}
	return entries, s.writeLocked(agent, nil)
}

// Rename moves oldAgent's entries to newAgent. It refuses
// (ErrNameHasMessages) if newAgent has entries of its own, even when
// oldAgent has none: they were queued for whoever held that name before (a
// deleted agent whose drop failed), so merging would hand them to the wrong
// agent, and the agent's own would be mixed with them. They stay for a
// person to deal with, and the error names their file.
func (s *Store) Rename(oldAgent, newAgent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked(oldAgent)
	if err != nil {
		return err
	}
	if err := s.checkEmptyLocked(newAgent); err != nil {
		return fmt.Errorf("outbox rename %s to %s: %w", oldAgent, newAgent, err)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := s.writeLocked(newAgent, entries); err != nil {
		return err
	}
	return s.writeLocked(oldAgent, nil)
}

// CheckEmpty fails with ErrNameHasMessages, naming their file, if agent
// has messages queued: a name a brand-new agent may not take, for its first
// launch would carry them (see Rename).
func (s *Store) CheckEmpty(agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkEmptyLocked(agent)
}

func (s *Store) checkEmptyLocked(agent string) error {
	entries, err := s.readLocked(agent)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	path, _ := s.path(agent)
	return fmt.Errorf("%w: %d in %s", ErrNameHasMessages, len(entries), path)
}

func (s *Store) path(agent string) (string, error) {
	bad := agent == "" || agent == "." || agent == ".." || strings.ContainsAny(agent, "/\\\x00")
	if bad {
		return "", fmt.Errorf("%w: %q", ErrInvalidAgent, agent)
	}
	return filepath.Join(s.dir, agent+".json"), nil
}

func (s *Store) readLocked(agent string) ([]Entry, error) {
	path, err := s.path(agent)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the store dir plus a validated agent name
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the outbox of %s: %w", agent, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, s.moveAsideLocked(agent, path, err)
	}
	return f.Entries, nil
}

// moveAsideLocked sets agent's corrupt file (cause says why) aside, whole,
// as <file>.corrupt-<time>, so the agent goes on with an empty outbox
// instead of being stuck behind it, and logs where it went: its messages
// are recoverable only by hand. A file that cannot be moved stays an error,
// never overwritten.
func (s *Store) moveAsideLocked(agent, path string, cause error) error {
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("the outbox of %s is corrupt (%v) and could not be moved aside: %w", agent, cause, err)
	}
	s.logf("outbox: ERROR: the outbox of %s was corrupt (%v); moved it to %s and continuing with an empty one. Its undelivered messages are only in that file now.", agent, cause, aside)
	return nil
}

// writeLocked stores entries as agent's file, or removes the file when
// there are none. The file is replaced whole: a crash leaves the old one or
// the new one, never a mix.
func (s *Store) writeLocked(agent string, entries []Entry) error {
	path, err := s.path(agent)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clearing the outbox of %s: %w", agent, err)
		}
		return nil
	}
	data, err := json.Marshal(file{Entries: entries})
	if err != nil {
		return fmt.Errorf("encoding the outbox of %s: %w", agent, err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating the outbox dir: %w", err)
	}
	if err := writeFileAtomic(s.dir, path, data); err != nil {
		return fmt.Errorf("writing the outbox of %s: %w", agent, err)
	}
	return nil
}

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

func textBytes(entries []Entry) int64 {
	var n int64
	for _, e := range entries {
		n += int64(len(e.Text))
	}
	return n
}
