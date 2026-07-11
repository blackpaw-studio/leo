package agent

import (
	"log"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// agentIDs adapts an agentstore.Record's SessionID field to
// harness.SessionIDStore for a DriveTurns driver (e.g. codex thread ids).
type agentIDs struct {
	homePath string
	name     string
}

// NewAgentIDs returns a harness.SessionIDStore backed by the agentstore
// record for name. Get returns "" and Set/Clear are no-ops (logging a
// warning) when no record exists for name yet — callers may build a handle
// before the first agentstore.Save. Exported so internal/service can share
// this single implementation instead of maintaining its own duplicate (see
// agentOrProcessIDs in internal/service/idstore.go).
func NewAgentIDs(homePath, name string) harness.SessionIDStore {
	return &agentIDs{homePath: homePath, name: name}
}

func (a *agentIDs) Get() string {
	rec, ok := a.load()
	if !ok {
		return ""
	}
	return rec.SessionID
}

func (a *agentIDs) Set(id string) {
	rec, ok := a.load()
	if !ok {
		log.Printf("agent %q: cannot persist session id, no agentstore record", a.name)
		return
	}
	rec.SessionID = id
	if err := agentstore.Save(a.homePath, rec); err != nil {
		log.Printf("agent %q: could not persist session id: %v", a.name, err)
	}
}

func (a *agentIDs) Clear() {
	a.Set("")
}

func (a *agentIDs) load() (agentstore.Record, bool) {
	records, err := agentstore.Load(agentstore.FilePath(a.homePath))
	if err != nil {
		return agentstore.Record{}, false
	}
	rec, ok := records[a.name]
	return rec, ok
}
