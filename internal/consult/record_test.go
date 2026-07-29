package consult

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a clock advancing one second per call, so stream
// timestamps are deterministic without sleeping. It is mutex-guarded
// because FileRecorder.Now is called from every recording goroutine.
func fixedClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	n := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		n++
		return start.Add(time.Duration(n) * time.Second)
	}
}

func newTestRecorder(t *testing.T) (*FileRecorder, string) {
	t.Helper()
	state := t.TempDir()
	r := NewFileRecorder(state)
	r.Now = fixedClock(time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC))
	return r, filepath.Join(state, "consults")
}

func testRecord(id string) Record {
	return Record{
		ID: id, Caller: "leo", Template: "codex", Harness: "codex",
		Model: "gpt-5.3-codex", Prompt: "what do you think?",
		Status: StatusQueued, StartedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}
}

func mustReadRecord(t *testing.T, dir, id string) Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("reading record: %v", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decoding record: %v", err)
	}
	return rec
}

func readStream(t *testing.T, dir, id string) []streamEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".ndjson"))
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	var events []streamEvent
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decoding stream line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestFileRecorderWritesRecordAndFramedStream(t *testing.T) {
	r, dir := newTestRecorder(t)
	h, err := r.Open(testRecord("c-0001"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if got := mustReadRecord(t, dir, "c-0001"); got.Status != StatusQueued || got.Prompt != "what do you think?" {
		t.Fatalf("record at open = %+v, want queued with prompt", got)
	}

	if _, err := h.Write([]byte(`{"type":"result","result":"ok"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := h.Write([]byte("codex: connection reset\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readStream(t, dir, "c-0001")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if string(events[0].D) != `{"type":"result","result":"ok"}` {
		t.Errorf("event 0 d = %s, want the raw harness event verbatim", events[0].D)
	}
	if events[0].Raw != "" {
		t.Errorf("event 0 raw = %q, want empty for a parseable line", events[0].Raw)
	}
	if events[1].Raw != "codex: connection reset" || len(events[1].D) != 0 {
		t.Errorf("event 1 = %+v, want the non-JSON line captured as raw", events[1])
	}
	if !(events[0].T > 0 && events[1].T > events[0].T) {
		t.Errorf("timestamps %v, %v: want increasing offsets from start", events[0].T, events[1].T)
	}

	rec := mustReadRecord(t, dir, "c-0001")
	if rec.Status != StatusDone {
		t.Errorf("status = %q, want done", rec.Status)
	}
	if rec.EndedAt.IsZero() {
		t.Error("ended_at unset on a terminal status")
	}
}

func TestFileRecorderFramesAcrossPartialWrites(t *testing.T) {
	r, dir := newTestRecorder(t)
	h, err := r.Open(testRecord("c-0002"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A pipe hands over arbitrary chunks, not whole lines.
	for _, chunk := range []string{`{"type":"te`, `xt","body":"hi"}` + "\n" + `{"type":"e`, `nd"}` + "\n"} {
		if _, err := h.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readStream(t, dir, "c-0002")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 reassembled lines: %+v", len(events), events)
	}
	if string(events[0].D) != `{"type":"text","body":"hi"}` {
		t.Errorf("event 0 = %s, want the reassembled line", events[0].D)
	}
	if string(events[1].D) != `{"type":"end"}` {
		t.Errorf("event 1 = %s, want the reassembled line", events[1].D)
	}
}

func TestFileRecorderFlushesTrailingPartialLineOnClose(t *testing.T) {
	r, dir := newTestRecorder(t)
	h, err := r.Open(testRecord("c-0003"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A crashing harness can die mid-line, with no trailing newline.
	if _, err := h.Write([]byte("panic: runtime error")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := h.Close(StatusFailed, errors.New("exit status 2")); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readStream(t, dir, "c-0003")
	if len(events) != 1 || events[0].Raw != "panic: runtime error" {
		t.Fatalf("got %+v, want the trailing partial line flushed", events)
	}
	if rec := mustReadRecord(t, dir, "c-0003"); rec.Status != StatusFailed || rec.Error != "exit status 2" {
		t.Errorf("record = %+v, want failed with the error recorded", rec)
	}
}

func TestFileRecorderSetStatusIsVisibleOnDisk(t *testing.T) {
	r, dir := newTestRecorder(t)
	h, err := r.Open(testRecord("c-0004"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := h.SetStatus(StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := mustReadRecord(t, dir, "c-0004").Status; got != StatusRunning {
		t.Fatalf("status = %q, want running", got)
	}
	if rec := mustReadRecord(t, dir, "c-0004"); !rec.EndedAt.IsZero() {
		t.Error("ended_at set on a non-terminal status")
	}
	_ = h.Close(StatusDone, nil)
}

func TestFileRecorderPrunesOldestTerminalRecords(t *testing.T) {
	r, dir := newTestRecorder(t)
	for i := range RecordsKept + 3 {
		rec := testRecord(fmt.Sprintf("c-%04d", i))
		rec.StartedAt = rec.StartedAt.Add(time.Duration(i) * time.Minute)
		h, err := r.Open(rec)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := h.Close(StatusDone, nil); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}

	records, err := Load(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != RecordsKept {
		t.Fatalf("kept %d records, want %d", len(records), RecordsKept)
	}
	if records[0].ID != fmt.Sprintf("c-%04d", RecordsKept+2) {
		t.Errorf("newest record = %q, want the last one opened", records[0].ID)
	}
	// The pruned records take their streams with them.
	if _, err := os.Stat(filepath.Join(dir, "c-0000.ndjson")); !os.IsNotExist(err) {
		t.Error("pruned record's stream file survived")
	}
}

func TestLoadSkipsUnreadableRecords(t *testing.T) {
	r, dir := newTestRecorder(t)
	h, err := r.Open(testRecord("c-good"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c-torn.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing torn record: %v", err)
	}

	records, err := Load(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 1 || records[0].ID != "c-good" {
		t.Fatalf("got %+v, want only the readable record", records)
	}
}

func TestLoadOnMissingDirectoryIsEmpty(t *testing.T) {
	records, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want none", len(records))
	}
}

func TestNopRecorderAcceptsEverything(t *testing.T) {
	h, err := nopRecorder{}.Open(testRecord("c-nop"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if n, err := h.Write([]byte("anything")); err != nil || n != 8 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := h.SetStatus(StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestFileRecorderPrunesAbandonedRecords covers the record a SIGKILLed
// daemon leaves stuck at running: without staleness it would occupy a slot
// forever and the retention budget would not be a bound.
func TestFileRecorderPrunesAbandonedRecords(t *testing.T) {
	r, dir := newTestRecorder(t)
	abandoned := testRecord("c-stuck")
	abandoned.Status = StatusRunning
	abandoned.StartedAt = abandoned.StartedAt.Add(-(StaleAfter + time.Hour))
	if err := writeRecord(mkdir(t, dir), abandoned); err != nil {
		t.Fatalf("seeding abandoned record: %v", err)
	}

	for i := range RecordsKept {
		rec := testRecord(fmt.Sprintf("c-%04d", i))
		rec.StartedAt = rec.StartedAt.Add(time.Duration(i) * time.Minute)
		h, err := r.Open(rec)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := h.Close(StatusDone, nil); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "c-stuck.json")); !os.IsNotExist(err) {
		t.Error("abandoned record survived pruning and holds a slot forever")
	}
}

// TestFileRecorderKeepsFreshUnfinishedRecords is the other half: a consult
// genuinely in flight must never be evicted, however full the directory is.
func TestFileRecorderKeepsFreshUnfinishedRecords(t *testing.T) {
	r, dir := newTestRecorder(t)
	live, err := r.Open(testRecord("c-alive"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := live.SetStatus(StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	for i := range RecordsKept + 3 {
		rec := testRecord(fmt.Sprintf("c-%04d", i))
		rec.StartedAt = rec.StartedAt.Add(time.Duration(i) * time.Minute)
		h, err := r.Open(rec)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := h.Close(StatusDone, nil); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "c-alive.json")); err != nil {
		t.Fatalf("consult still in flight was pruned: %v", err)
	}
	_ = live.Close(StatusDone, nil)
}

// TestFileRecorderReclaimsGarbage: an unreadable record and an orphaned
// stream are otherwise never collected, so the directory grows forever.
func TestFileRecorderReclaimsGarbage(t *testing.T) {
	r, dir := newTestRecorder(t)
	mkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "c-corrupt.json"), []byte("{not json"), filePerm); err != nil {
		t.Fatalf("seeding corrupt record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c-corrupt.ndjson"), []byte("{}\n"), filePerm); err != nil {
		t.Fatalf("seeding corrupt stream: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c-orphan.ndjson"), []byte("{}\n"), filePerm); err != nil {
		t.Fatalf("seeding orphan stream: %v", err)
	}
	stale := filepath.Join(dir, "c-old.json.tmp")
	if err := os.WriteFile(stale, []byte("{}"), filePerm); err != nil {
		t.Fatalf("seeding temp file: %v", err)
	}
	old := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("ageing temp file: %v", err)
	}

	h, err := r.Open(testRecord("c-fresh"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, name := range []string{"c-corrupt.json", "c-corrupt.ndjson", "c-orphan.ndjson", "c-old.json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s was never reclaimed", name)
		}
	}
}

// TestFileRecorderRefusesToClobberALiveStream: an id collision must fail
// loudly rather than truncate someone else's recording.
func TestFileRecorderRefusesToClobberALiveStream(t *testing.T) {
	r, _ := newTestRecorder(t)
	first, err := r.Open(testRecord("c-dupe"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.Write([]byte(`{"type":"keep"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := r.Open(testRecord("c-dupe")); err == nil {
		t.Fatal("a colliding id silently reused the live stream")
	}
}

// TestFileRecorderUnderConcurrentConsults is the spec's race guard: up to
// maxConcurrent consults record at once.
func TestFileRecorderUnderConcurrentConsults(t *testing.T) {
	r, dir := newTestRecorder(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := testRecord(fmt.Sprintf("c-race%02d", i))
			h, err := r.Open(rec)
			if err != nil {
				t.Errorf("Open %d: %v", i, err)
				return
			}
			if err := h.SetStatus(StatusRunning); err != nil {
				t.Errorf("SetStatus %d: %v", i, err)
			}
			for range 20 {
				if _, err := fmt.Fprintf(h, `{"i":%d}`+"\n", i); err != nil {
					t.Errorf("Write %d: %v", i, err)
				}
			}
			if err := h.Close(StatusDone, nil); err != nil {
				t.Errorf("Close %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	// Each consult's stream holds exactly its own events, uninterleaved.
	for i := range 8 {
		id := fmt.Sprintf("c-race%02d", i)
		if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
			continue // legitimately pruned; the budget is 20
		}
		for _, ev := range readStream(t, dir, id) {
			if want := fmt.Sprintf(`{"i":%d}`, i); string(ev.D) != want {
				t.Fatalf("%s recorded %s, want %s", id, ev.D, want)
			}
		}
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}
