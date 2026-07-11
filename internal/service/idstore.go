package service

import (
	"fmt"
	"os"

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

// agentstoreIDs adapts an agentstore.Record's SessionID field to
// harness.SessionIDStore for ephemeral agents driven by a DriveTurns
// harness. Mirrors agent.newAgentIDs (unexported there; duplicated here
// because service cannot reach an unexported symbol across the package
// boundary) — both read/write agentstore.Record.SessionID.
type agentstoreIDs struct {
	homePath string
	name     string
}

func (a *agentstoreIDs) Get() string {
	rec, ok := a.load()
	if !ok {
		return ""
	}
	return rec.SessionID
}

func (a *agentstoreIDs) Set(id string) {
	rec, ok := a.load()
	if !ok {
		fmt.Fprintf(os.Stderr, "warning: agent %q: cannot persist session id, no agentstore record\n", a.name)
		return
	}
	rec.SessionID = id
	if err := agentstore.Save(a.homePath, rec); err != nil {
		fmt.Fprintf(os.Stderr, "warning: agent %q: could not persist session id: %v\n", a.name, err)
	}
}

func (a *agentstoreIDs) Clear() {
	a.Set("")
}

func (a *agentstoreIDs) load() (agentstore.Record, bool) {
	records, err := agentstore.Load(agentstore.FilePath(a.homePath))
	if err != nil {
		return agentstore.Record{}, false
	}
	rec, ok := records[a.name]
	return rec, ok
}

// agentOrProcessIDs picks the agentstore-backed store when an agentstore
// record exists for name (ephemeral agent), else falls back to the
// session-store "process:<name>" key (config-defined process).
func agentOrProcessIDs(homePath, name string) harness.SessionIDStore {
	if records, err := agentstore.Load(agentstore.FilePath(homePath)); err == nil {
		if _, ok := records[name]; ok {
			return &agentstoreIDs{homePath: homePath, name: name}
		}
	}
	return newStoreIDs(homePath, "process:"+name)
}
