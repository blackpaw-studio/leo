package service

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

// sessionStoreIDs adapts internal/session.Store to harness.SessionIDStore for
// supervised units backed by session.NewStore(homePath) — config-defined
// processes ("process:<name>") and persistent task sessions ("session:<name>").
type sessionStoreIDs struct {
	store *session.Store
	key   string
}

// newStoreIDs returns a harness.SessionIDStore over session.NewStore(homePath),
// keyed by key.
func newStoreIDs(homePath, key string) harness.SessionIDStore {
	return &sessionStoreIDs{store: session.NewStore(homePath), key: key}
}

func (s *sessionStoreIDs) Get() string {
	id, _, err := s.store.Get(s.key)
	if err != nil {
		return ""
	}
	return id
}

func (s *sessionStoreIDs) Set(id string) {
	if err := s.store.Set(s.key, id); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist session id for %q: %v\n", s.key, err)
	}
}

func (s *sessionStoreIDs) Clear() {
	if err := s.store.Delete(s.key); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not clear session id for %q: %v\n", s.key, err)
	}
}

// agentOrProcessIDs picks the agentstore-backed store when an agentstore
// record exists for name (ephemeral agent), else falls back to the
// session-store "process:<name>" key (config-defined process). The
// agentstore-backed implementation lives in agent.NewAgentIDs — service
// reuses it rather than maintaining its own duplicate adapter.
func agentOrProcessIDs(homePath, name string) harness.SessionIDStore {
	if records, err := agentstore.Load(agentstore.FilePath(homePath)); err == nil {
		if _, ok := records[name]; ok {
			return agent.NewAgentIDs(homePath, name)
		}
	}
	return newStoreIDs(homePath, "process:"+name)
}
