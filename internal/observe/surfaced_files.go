package observe

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// MaxSurfacedFiles caps how many surfaced files each agent keeps; the oldest
// are dropped first.
const MaxSurfacedFiles = 20

// MaxSurfaceReasonRunes caps a surfaced file's reason, in Unicode code points.
// Longer reasons are rejected, never truncated.
const MaxSurfaceReasonRunes = 200

// ErrStaleIncarnation rejects a submission made by an agent generation that
// has since been replaced (its StartedAt is older than the store's).
var ErrStaleIncarnation = errors.New("agent restarted since the request was made")

// SurfacedFile is one file an agent pushed to the user's attention. The same
// object is the file_surfaced event body and an entry in the agent's
// surfaced_files state, so the two always agree — including At.
type SurfacedFile struct {
	Type      EventType `json:"type"`
	Agent     string    `json:"agent"`
	StartedAt time.Time `json:"started_at"`
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	AbsPath   string    `json:"abs_path"`
	Line      int       `json:"line,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	At        time.Time `json:"at"`
}

// FileSurfacedPayload is the file_surfaced event. It carries Seq but not
// Meta: the surfaced file's own At is the event time, so the bus must not
// replace it with its publish time.
type FileSurfacedPayload struct {
	Seq uint64 `json:"seq"`
	SurfacedFile
}

func (p *FileSurfacedPayload) stamp(seq uint64, _ time.Time) { p.Seq = seq }

// SurfaceInput is a validated submission: Path as the agent gave it and the
// AbsPath it resolved to.
type SurfaceInput struct {
	Path    string
	AbsPath string
	Line    int
	Reason  string
}

type surfacedAgent struct {
	startedAt time.Time
	files     []SurfacedFile
}

// SurfacedFileStore holds each agent's recently surfaced files in memory,
// scoped to one incarnation (StartedAt) of the agent. Methods are safe for
// concurrent use, return copies, and are nil-safe.
type SurfacedFileStore struct {
	mu        sync.Mutex
	agents    map[string]*surfacedAgent
	publisher Publisher
	now       func() time.Time
}

// NewSurfacedFileStore creates an empty store. publisher may be nil; now
// defaults to time.Now.
func NewSurfacedFileStore(publisher Publisher, now func() time.Time) *SurfacedFileStore {
	if now == nil {
		now = time.Now
	}
	return &SurfacedFileStore{agents: make(map[string]*surfacedAgent), publisher: publisher, now: now}
}

// Add records a file for agent's incarnation startedAt and publishes it.
// A startedAt older than the store's current one is rejected with
// ErrStaleIncarnation; a newer one starts a fresh list.
func (s *SurfacedFileStore) Add(agent string, startedAt time.Time, in SurfaceInput) (SurfacedFile, error) {
	if s == nil {
		return SurfacedFile{}, errors.New("surfaced files are not available")
	}
	id, err := newUUIDv4()
	if err != nil {
		return SurfacedFile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.agents[agent]
	switch {
	case entry == nil || startedAt.After(entry.startedAt):
		entry = &surfacedAgent{startedAt: startedAt}
		s.agents[agent] = entry
	case startedAt.Before(entry.startedAt):
		return SurfacedFile{}, ErrStaleIncarnation
	}
	file := SurfacedFile{
		Type: EventFileSurfaced, Agent: agent, StartedAt: startedAt, ID: id,
		Path: in.Path, AbsPath: in.AbsPath, Line: in.Line, Reason: in.Reason, At: s.now(),
	}
	entry.files = append(entry.files, file)
	if over := len(entry.files) - MaxSurfacedFiles; over > 0 {
		entry.files = slices.Clone(entry.files[over:])
	}
	if s.publisher != nil {
		s.publisher.Publish(Event{Type: EventFileSurfaced, Payload: &FileSurfacedPayload{SurfacedFile: file}})
	}
	return file, nil
}

// Reset moves agent to incarnation startedAt, dropping the previous one's
// files. The same or an older startedAt is a no-op.
func (s *SurfacedFileStore) Reset(agent string, startedAt time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.agents[agent]; entry != nil && !startedAt.After(entry.startedAt) {
		return
	}
	s.agents[agent] = &surfacedAgent{startedAt: startedAt}
}

// Remove forgets agent entirely (delete, rename).
func (s *SurfacedFileStore) Remove(agent string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.agents, agent)
	s.mu.Unlock()
}

// Get returns agent's files, oldest first, or nil.
func (s *SurfacedFileStore) Get(agent string) []SurfacedFile {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.agents[agent]; entry != nil && len(entry.files) > 0 {
		return slices.Clone(entry.files)
	}
	return nil
}

// All returns every agent's non-empty file list, oldest first.
func (s *SurfacedFileStore) All() map[string][]SurfacedFile {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]SurfacedFile, len(s.agents))
	for name, entry := range s.agents {
		if len(entry.files) > 0 {
			out[name] = slices.Clone(entry.files)
		}
	}
	return out
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
