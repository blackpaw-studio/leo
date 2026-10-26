package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// What a hook-injected delegation context says. The daemon owns the text:
// the `leo dispatch context` hook command only prints what it is handed.
const (
	delegationReplacedNote = "leo: delegation settings changed; this replaces the leo-delegation section of your system prompt.\n\n"
	delegationOffNote      = "leo: delegation is now OFF. Ignore the leo-delegation section of your system prompt; native agents are allowed."
)

const (
	// toldMaxAge is how long an untouched told entry lives. A live session
	// restamps its own (toldRestamp), so only a session long gone ages out.
	toldMaxAge  = 7 * 24 * time.Hour
	toldRestamp = time.Hour
)

// delegationTold is what one session's model was last told about delegation.
// Hash covers the section text only while Enabled: two policies read the
// same to the model when both are off, or both on with the same section.
type delegationTold struct {
	Enabled bool      `json:"enabled"`
	Hash    string    `json:"hash,omitempty"`
	At      time.Time `json:"at"`
}

// toldFor is the told record for delegation as of now.
func toldFor(d bridge.DelegationState, now time.Time) delegationTold {
	if !isDelegationOn(d) {
		return delegationTold{At: now}
	}
	sum := sha256.Sum256([]byte(d.Section))
	return delegationTold{Enabled: true, Hash: hex.EncodeToString(sum[:]), At: now}
}

func isDelegationOn(d bridge.DelegationState) bool { return d.Enabled && d.Section != "" }

// sameToldDelegation reports whether told reads the same to the model as d.
func sameToldDelegation(told delegationTold, d bridge.DelegationState) bool {
	want := toldFor(d, time.Time{})
	return told.Enabled == want.Enabled && told.Hash == want.Hash
}

// contextFor is the delegation context a Claude hook adds to the model's
// context, and the told record that follows from emitting it. told is nil
// when the session was never told.
//
// SessionStart (startup, resume, clear, compact and fork alike: the model's
// context was just rebuilt, so what it was told may be gone) emits the
// section while delegation is on, and an OFF note when it was told on and
// is now off. UserPromptSubmit emits only when the policy differs from what
// the session was told; a session never told that delegation is on hears
// nothing while it is off.
func contextFor(event, source string, current bridge.DelegationState, told *delegationTold, now time.Time) (string, delegationTold) {
	next := toldFor(current, now)
	if told != nil && sameToldDelegation(*told, current) && now.Sub(told.At) < toldRestamp {
		next = *told
	}
	switch event {
	case "SessionStart":
		switch {
		case isDelegationOn(current):
			return current.Section, next
		case told != nil && told.Enabled:
			return delegationOffNote, next
		}
	case "UserPromptSubmit":
		switch {
		case told != nil && sameToldDelegation(*told, current):
		case isDelegationOn(current):
			return delegationReplacedNote + current.Section, next
		case told != nil:
			return delegationOffNote, next
		}
	default:
		if told != nil {
			return "", *told
		}
	}
	return "", next
}

// toldStore remembers what each session was told, by agent and session id,
// in one JSON file written atomically. A missing or unreadable file reads as
// empty: the cost is one repeated injection, never a refused hook.
type toldStore struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	loaded  bool
	entries map[string]delegationTold
}

func newToldStore(path string, now func() time.Time) *toldStore {
	return &toldStore{path: path, now: now}
}

func toldKey(agent, session string) string { return agent + "\x00" + session }

// Get returns what agent's session was told.
func (s *toldStore) Get(agent, session string) (delegationTold, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	t, ok := s.entries[toldKey(agent, session)]
	return t, ok
}

// Put records what agent's session was told and persists the store, dropping
// entries untouched for toldMaxAge.
func (s *toldStore) Put(agent, session string, t delegationTold) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	next := make(map[string]delegationTold, len(s.entries)+1)
	cutoff := s.now().Add(-toldMaxAge)
	for key, entry := range s.entries {
		if entry.At.After(cutoff) {
			next[key] = entry
		}
	}
	next[toldKey(agent, session)] = t
	encoded, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encoding told delegation: %w", err)
	}
	if err := writeToldFile(s.path, encoded); err != nil {
		return fmt.Errorf("writing told delegation: %w", err)
	}
	s.entries = next
	return nil
}

func (s *toldStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.entries = map[string]delegationTold{}
	raw, err := os.ReadFile(s.path) // #nosec G304 -- daemon-owned state file
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "delegation: reading told state: %v\n", err)
		}
		return
	}
	var entries map[string]delegationTold
	if err := json.Unmarshal(raw, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "delegation: ignoring unreadable told state: %v\n", err)
		return
	}
	s.entries = entries
}

// writeToldFile replaces path with data, readable only by the owner, via a
// same-directory temp file and rename so a reader never sees a partial write.
func writeToldFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".told-*.tmp") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
