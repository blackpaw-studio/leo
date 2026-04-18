package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// sessionTTL is the lifetime of a browser login. Not user-configurable in the
// first cut — solo-user tool, a week is a reasonable default.
const sessionTTL = 7 * 24 * time.Hour

// sessionStore is an in-memory map of session ID -> expiry. Sessions are lost
// on daemon restart; the user logs in again. No on-disk persistence.
type sessionStore struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{ttl: ttl, m: make(map[string]time.Time)}
}

func (s *sessionStore) create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session: rand: %w", err)
	}
	id := hex.EncodeToString(buf)
	s.mu.Lock()
	s.m[id] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return id, nil
}

// validate returns true iff id is non-empty, present, and not expired. Lookup
// is constant-time against the set of live session IDs to avoid a timing
// side channel on session ID guessing. Entries are also swept when visited.
func (s *sessionStore) validate(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var matched bool
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k), []byte(id)) == 1 {
			matched = true
		}
	}
	return matched
}

func (s *sessionStore) destroy(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}
