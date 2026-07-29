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
	// Now supplies stream timestamps; replaced in tests.
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

	path := filepath.Join(r.dir, rec.ID+".ndjson")
	stream, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
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

// prune deletes the oldest finished consults until at most keep remain.
// Consults that have not reached a terminal status are skipped: evicting a
// running consult would delete the stream someone is watching.
func (r *FileRecorder) prune(keep int) {
	records, err := loadDir(r.dir)
	if err != nil {
		return
	}
	finished := make([]Record, 0, len(records))
	for _, rec := range records {
		if rec.Status.Terminal() {
			finished = append(finished, rec)
		}
	}
	if len(finished) <= keep {
		return
	}
	// loadDir sorts newest first, so everything past the budget is oldest.
	for _, rec := range finished[keep:] {
		_ = os.Remove(filepath.Join(r.dir, rec.ID+".json"))
		_ = os.Remove(filepath.Join(r.dir, rec.ID+".ndjson"))
	}
}

// fileHandle records one consult. Every method is mutex-guarded because the
// tee writes from the process-output goroutine while the dispatcher updates
// status from its own.
type fileHandle struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	rec      Record
	stream   *os.File
	pending  bytes.Buffer
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
	h.pending.Write(p)
	for {
		line, err := h.pending.ReadBytes('\n')
		if err != nil {
			// No delimiter yet — a pipe hands over arbitrary chunks, not
			// whole lines. Hold the remainder for the next write.
			h.pending.Reset()
			h.pending.Write(line)
			break
		}
		h.emit(line)
	}
	if h.pending.Len() > maxLineBytes {
		h.emit(h.pending.Bytes())
		h.pending.Reset()
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
	if h.pending.Len() > 0 {
		h.emit(h.pending.Bytes())
		h.pending.Reset()
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
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil || rec.ID == "" {
			continue
		}
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b Record) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return records, nil
}
