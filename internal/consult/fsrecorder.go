package consult

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// RecordsKept bounds the consult directory, mirroring
	// history.maxHistoryPerTask. Consults still in flight are never
	// evicted, so the directory can briefly hold a few more.
	RecordsKept = 20

	consultsDirName = "consults"
	dirPerm         = 0o700
	filePerm        = 0o600

	// maxLineBytes caps in-memory line reassembly so a harness that never
	// emits a newline cannot grow the buffer without bound. A tool result
	// echoing a large file is legitimately big, so the cap is generous.
	maxLineBytes = 8 << 20

	// tmpReapAfter is how old an unrenamed record temp file must be before
	// it is presumed abandoned. Another consult may be mid-rename, so only
	// clearly stale ones are reaped.
	tmpReapAfter = time.Minute
)

// Dir returns the directory holding consult records for a leo state dir.
func Dir(stateDir string) string { return filepath.Join(stateDir, consultsDirName) }

// StreamPath returns the path of a consult's event stream.
func StreamPath(stateDir, id string) string {
	return filepath.Join(Dir(stateDir), id+".ndjson")
}

// FileRecorder persists consults under <state>/consults as an <id>.json
// record plus an <id>.ndjson event stream.
type FileRecorder struct {
	dir string
	// Now supplies stream timestamps and decides staleness; replaced in
	// tests. It must be safe for concurrent use: every recording goroutine
	// calls it, as does pruning.
	Now func() time.Time
	// mu serializes pruning against concurrent Opens — up to maxConcurrent
	// consults start independently.
	mu sync.Mutex
}

func NewFileRecorder(stateDir string) *FileRecorder {
	return &FileRecorder{dir: Dir(stateDir), Now: time.Now}
}

func (r *FileRecorder) Open(rec Record) (Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(r.dir, dirPerm); err != nil {
		return nil, fmt.Errorf("creating consult directory: %w", err)
	}
	// Make room before adding, so the directory settles at RecordsKept.
	r.prune(RecordsKept - 1)

	// O_EXCL rather than O_TRUNC: on an id collision, fail loudly instead of
	// silently truncating a live consult's stream. The caller degrades an
	// Open failure to "not recorded", which is the better of the two.
	path := filepath.Join(r.dir, rec.ID+".ndjson")
	stream, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return nil, fmt.Errorf("creating consult stream: %w", err)
	}
	h := &fileHandle{dir: r.dir, rec: rec, stream: stream, now: r.Now}
	if err := h.persist(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return h, nil
}

// prune bounds the consult directory: it reclaims garbage, then deletes
// the oldest settled consults until at most keep remain.
//
// A consult still plausibly in flight is never evicted — that would delete
// the stream someone is watching. "Plausibly" is doing real work: a record
// abandoned by a killed daemon stays non-terminal forever, so staleness,
// not just terminal status, decides.
func (r *FileRecorder) prune(keep int) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	now := r.Now()

	// Partition the directory before deleting anything.
	records := make([]Record, 0, len(entries))
	haveRecord := make(map[string]bool, len(entries))
	var corrupt, streams, temps []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".json.tmp"):
			temps = append(temps, name)
		case strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(name, ".json")
			haveRecord[id] = true
			rec, err := readRecord(filepath.Join(r.dir, name))
			if err != nil {
				// A record is published by rename, so it is never seen
				// half-written. Unreadable means corrupt, and neither it
				// nor its stream will ever be reclaimed otherwise.
				corrupt = append(corrupt, id)
				continue
			}
			records = append(records, rec)
		case strings.HasSuffix(name, ".ndjson"):
			streams = append(streams, strings.TrimSuffix(name, ".ndjson"))
		}
	}

	for _, id := range corrupt {
		r.remove(id)
		haveRecord[id] = false
	}
	// A stream whose record is gone can never be found again. Open holds
	// r.mu across creating both files, so this cannot race a starting
	// consult.
	for _, id := range streams {
		if !haveRecord[id] {
			_ = os.Remove(filepath.Join(r.dir, id+".ndjson"))
		}
	}
	for _, name := range temps {
		info, err := os.Stat(filepath.Join(r.dir, name))
		if err == nil && now.Sub(info.ModTime()) > tmpReapAfter {
			_ = os.Remove(filepath.Join(r.dir, name))
		}
	}

	settled := make([]Record, 0, len(records))
	for _, rec := range records {
		if rec.Settled(now) {
			settled = append(settled, rec)
		}
	}
	if len(settled) <= keep {
		return
	}
	sortNewestFirst(settled)
	for _, rec := range settled[keep:] {
		r.remove(rec.ID)
	}
}

func (r *FileRecorder) remove(id string) {
	_ = os.Remove(filepath.Join(r.dir, id+".json"))
	_ = os.Remove(filepath.Join(r.dir, id+".ndjson"))
}

// fileHandle records one consult. Every method is mutex-guarded because the
// tee writes from the process-output goroutine while the dispatcher updates
// status from its own.
type fileHandle struct {
	dir string
	now func() time.Time

	mu  sync.Mutex
	rec Record
	// pending holds a line still being reassembled. It is resliced forward
	// rather than rewritten, so a single huge line arriving in pipe-sized
	// chunks does not cost a copy per chunk.
	pending  []byte
	stream   *os.File
	writeErr error
	closed   bool
}

// Write frames the harness's raw output into timestamped lines. It never
// reports an error: recording is best-effort and must not disturb the
// consult. The first failure is remembered and surfaced from Close.
func (h *fileHandle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return len(p), nil
	}
	// A pipe hands over arbitrary chunks, not whole lines; whatever trails
	// the last newline waits for the next write.
	h.pending = append(h.pending, p...)
	start := 0
	for {
		i := bytes.IndexByte(h.pending[start:], '\n')
		if i < 0 {
			break
		}
		h.emit(h.pending[start : start+i])
		start += i + 1
	}
	h.pending = h.pending[start:]
	if len(h.pending) > maxLineBytes {
		h.emit(h.pending)
		h.pending = h.pending[:0]
	}
	return len(p), nil
}

func (h *fileHandle) emit(line []byte) {
	line = bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	ev := streamEvent{T: h.offset()}
	if json.Valid(line) {
		ev.D = bytes.Clone(line)
	} else {
		ev.Raw = string(line)
	}
	encoded, err := json.Marshal(ev)
	if err != nil {
		h.note(fmt.Errorf("encoding consult event: %w", err))
		return
	}
	if _, err := h.stream.Write(append(encoded, '\n')); err != nil {
		h.note(fmt.Errorf("writing consult event: %w", err))
	}
}

// offset is seconds since the consult started, rounded to milliseconds.
func (h *fileHandle) offset() float64 {
	secs := h.now().Sub(h.rec.StartedAt).Seconds()
	return math.Round(secs*1000) / 1000
}

func (h *fileHandle) note(err error) {
	if h.writeErr == nil {
		h.writeErr = err
	}
}

func (h *fileHandle) SetStatus(s Status) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.rec.Status = s
	if s.Terminal() && h.rec.EndedAt.IsZero() {
		h.rec.EndedAt = h.now()
	}
	return h.persist()
}

func (h *fileHandle) Close(s Status, cause error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	// A harness can die mid-line; keep what it managed to say.
	if len(h.pending) > 0 {
		h.emit(h.pending)
		h.pending = nil
	}
	h.closed = true
	h.rec.Status = s
	h.rec.EndedAt = h.now()
	if cause != nil {
		h.rec.Error = cause.Error()
	}
	return errors.Join(h.writeErr, h.stream.Close(), h.persist())
}

func (h *fileHandle) persist() error { return writeRecord(h.dir, h.rec) }

// writeRecord publishes a record atomically: `leo consult list` reads these
// files while the daemon rewrites them on every status change.
func writeRecord(dir string, rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding consult record: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, rec.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, filePerm); err != nil {
		return fmt.Errorf("writing consult record: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, rec.ID+".json")); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publishing consult record: %w", err)
	}
	return nil
}

// Load returns every consult record under a leo state dir, newest first.
// Unreadable records are skipped: one torn or hand-edited file must not
// hide the rest.
func Load(stateDir string) ([]Record, error) { return loadDir(Dir(stateDir)) }

// LoadOne reads a single consult record. Callers polling one consult use
// this rather than Load, which decodes the whole directory.
func LoadOne(stateDir, id string) (Record, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return Record{}, fmt.Errorf("invalid consult id %q", id)
	}
	rec, err := readRecord(filepath.Join(Dir(stateDir), id+".json"))
	if err != nil {
		return Record{}, fmt.Errorf("reading consult %s: %w", id, err)
	}
	return rec, nil
}

func readRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, err
	}
	if rec.ID == "" {
		return Record{}, fmt.Errorf("record has no id")
	}
	return rec, nil
}

func loadDir(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading consult directory: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := readRecord(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		records = append(records, rec)
	}
	sortNewestFirst(records)
	return records, nil
}

func sortNewestFirst(records []Record) {
	slices.SortFunc(records, func(a, b Record) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
}
