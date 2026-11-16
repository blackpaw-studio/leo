package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// revisionReserve is how far past the highest revision issued the persisted
// ceiling sits. A restart resumes revisions above the ceiling, so it is above
// anything served before, while the file is rewritten for revisions only once
// per reserve rather than on every transition.
const revisionReserve = 4096

const attentionFileVersion = 1

// persistedAttention is one agent's at-rest attention as stored on disk. The
// token is the launch it belongs to: a restart restores it only for that
// launch (see Restore).
type persistedAttention struct {
	Token    string           `json:"token"`
	State    AttentionState   `json:"state"`
	Revision uint64           `json:"revision"`
	Reason   *AttentionReason `json:"reason,omitempty"`
	Preview  string           `json:"preview,omitempty"`
}

// attentionFile is the on-disk shape of the persisted attention.
type attentionFile struct {
	Version int `json:"version"`
	// RevisionCeiling is at least every revision issued by the lifetime that
	// wrote it.
	RevisionCeiling uint64                        `json:"revision_ceiling"`
	Agents          map[string]persistedAttention `json:"agents"`
}

// attentionPersister writes the store's at-rest attention to one file. Writes
// run on a goroutine woken by the store, never on its lock.
type attentionPersister struct {
	path string
	logf func(format string, args ...any)
	wake chan struct{}
	done chan struct{} // closed when the writer goroutine has exited

	mu   sync.Mutex // serializes snapshot+write so an older snapshot never lands last
	last []byte
}

// isAtRest reports whether state is one a restart restores. Working would
// strand an agent whose Stop hook was lost; unknown carries nothing.
func isAtRest(state AttentionState) bool {
	return state == AttentionFinished || state == AttentionNeedsInput || state == AttentionErrored
}

// EnablePersistence persists at-rest attention (finished, needs_input,
// errored) to path and loads what a previous lifetime left there for Restore.
// It also keeps revisions above everything that lifetime served. A missing
// file starts empty; an unreadable or corrupt one is logged and ignored. The
// file is rewritten after transitions until ctx ends. logf may be nil.
func (s *AttentionStore) EnablePersistence(ctx context.Context, path string, logf func(string, ...any)) {
	if s == nil || path == "" {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	prior := loadAttentionFile(path, logf)
	p := &attentionPersister{path: path, logf: logf, wake: make(chan struct{}, 1), done: make(chan struct{})}

	s.mu.Lock()
	s.persist = p
	s.floor = max(s.floor, prior.RevisionCeiling)
	s.ceiling = max(s.ceiling, s.floor)
	s.pending = prior.Agents
	s.mu.Unlock()
	// What a restart found on disk needs no rewrite until something changes.
	if data, err := json.Marshal(s.snapshotFile()); err == nil {
		p.mu.Lock()
		p.last = data
		p.mu.Unlock()
	}

	go func() {
		defer close(p.done)
		for {
			select {
			case <-ctx.Done():
				s.FlushPersistence()
				return
			case <-p.wake:
				s.FlushPersistence()
			}
		}
	}()
}

// WaitPersistence blocks until the writer started by EnablePersistence has
// exited after its context ended, its final flush included.
func (s *AttentionStore) WaitPersistence() {
	if s == nil {
		return
	}
	s.mu.Lock()
	p := s.persist
	s.mu.Unlock()
	if p != nil {
		<-p.done
	}
}

// FlushPersistence writes the current at-rest attention now. Transitions
// flush on their own shortly after; this is for shutdown and tests.
func (s *AttentionStore) FlushPersistence() {
	if s == nil {
		return
	}
	s.mu.Lock()
	p := s.persist
	s.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	data, err := json.Marshal(s.snapshotFile())
	if err != nil {
		p.logf("attention: encoding state: %v", err)
		return
	}
	if bytes.Equal(data, p.last) {
		return
	}
	if err := writeAttentionFile(p.path, data); err != nil {
		p.logf("attention: persisting state: %v", err)
		return
	}
	p.last = data
}

// snapshotFile is what the file should hold now: every tracked at-rest agent
// with a known launch token, plus the restorable records no adopt has claimed
// yet (so a flush before an agent is adopted does not lose its record).
func (s *AttentionStore) snapshotFile() attentionFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	agents := make(map[string]persistedAttention, len(s.states)+len(s.pending))
	for name, rec := range s.pending {
		agents[name] = rec
	}
	for name, state := range s.states {
		token := s.lastToken[name]
		if !isAtRest(state) || token == "" {
			delete(agents, name)
			continue
		}
		rec := persistedAttention{Token: token, State: state, Revision: s.revisions[name], Preview: s.previews[name]}
		if r, ok := s.reasons[name]; ok {
			rec.Reason = &r
		}
		agents[name] = rec
	}
	return attentionFile{Version: attentionFileVersion, RevisionCeiling: s.ceiling, Agents: agents}
}

// Restore re-establishes agent's persisted at-rest attention for the adopted
// launch carrying token, and reports whether it did. Only a record stored
// under the same agent name and token is restored: a fresh launch, a
// recreated agent or an empty token inherits nothing. Either way the record
// is consumed. Restoring publishes no event, so it never re-notifies. The
// restored attention is served at a revision above anything
// served before the restart. An agent that already has attention keeps it.
func (s *AttentionStore) Restore(agent, token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pending[agent]
	if !ok {
		return false
	}
	delete(s.pending, agent)
	s.markDirtyLocked()
	if _, tracked := s.states[agent]; tracked || token == "" || rec.Token != token {
		return false
	}
	s.lastToken[agent] = token
	if rec.Reason != nil {
		s.reasons[agent] = *rec.Reason
	}
	// Not published: the transition already happened, and was announced (and
	// pushed to phones), before the restart. Clients re-baseline from /state
	// and agent_spawned, which carry the restored attention.
	s.applyPreviewLocked(agent, rec.State, rec.Preview)
	return true
}

// nextRevisionLocked is agent's next revision: above its own counter and the
// floor a restart set, extending the persisted ceiling when passed.
func (s *AttentionStore) nextRevisionLocked(agent string) uint64 {
	rev := max(s.revisions[agent], s.floor) + 1
	s.revisions[agent] = rev
	s.reserveRevisionLocked(rev)
	return rev
}

// reserveRevisionLocked keeps the persisted ceiling above rev.
func (s *AttentionStore) reserveRevisionLocked(rev uint64) {
	if rev > s.ceiling {
		s.ceiling = rev + revisionReserve
		s.markDirtyLocked()
	}
}

// markDirtyLocked wakes the writer; a no-op without persistence, and never
// blocks (one pending wake covers any number of transitions).
func (s *AttentionStore) markDirtyLocked() {
	if s.persist == nil {
		return
	}
	select {
	case s.persist.wake <- struct{}{}:
	default:
	}
}

// loadAttentionFile reads a previous lifetime's file, keeping only records a
// restart may restore. Problems are logged and yield an empty result.
func loadAttentionFile(path string, logf func(string, ...any)) attentionFile {
	empty := attentionFile{Agents: map[string]persistedAttention{}}
	data, err := os.ReadFile(path) // #nosec G304 -- daemon-owned state path
	if errors.Is(err, fs.ErrNotExist) {
		return empty
	}
	if err != nil {
		logf("attention: reading %s: %v; starting empty", path, err)
		return empty
	}
	var f attentionFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != attentionFileVersion {
		logf("attention: %s is corrupt or unsupported (version %d, err %v); starting empty", path, f.Version, err)
		return empty
	}
	agents := make(map[string]persistedAttention, len(f.Agents))
	for name, rec := range f.Agents {
		if name != "" && rec.Token != "" && isAtRest(rec.State) {
			agents[name] = rec
		}
	}
	f.Agents = agents
	return f
}

// writeAttentionFile replaces path atomically (temp file, then rename) with
// owner-only permissions: previews carry assistant output.
func writeAttentionFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	committed = true
	return nil
}
