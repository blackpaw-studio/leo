package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/harness/claude"
)

// rec builds a record started minutesAgo minutes back. Recency is relative
// to now on purpose: an unfinished consult older than consult.StaleAfter is
// treated as abandoned, so a fixture pinned to a fixed date would rot into
// one the moment the wall clock moved past it.
func rec(id string, status consult.Status, minutesAgo int) consult.Record {
	return consult.Record{
		ID: id, Caller: "leo", Template: "codex", Harness: "codex", Model: "gpt-5.3-codex",
		Status: status, StartedAt: time.Now().Add(-time.Duration(minutesAgo) * time.Minute),
	}
}

func writeTestRecord(t *testing.T, dir string, record consult.Record) {
	t.Helper()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("encoding record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, record.ID+".json"), data, 0o600); err != nil {
		t.Fatalf("writing record: %v", err)
	}
}

func TestResolveConsult(t *testing.T) {
	records := []consult.Record{
		rec("c-ffff0001", consult.StatusDone, 1),
		rec("c-aaaa0002", consult.StatusRunning, 2),
		rec("c-aaaa0003", consult.StatusDone, 3),
	}
	tests := []struct {
		name    string
		prefix  string
		want    string
		wantErr string
	}{
		{name: "no argument prefers the running consult", want: "c-aaaa0002"},
		{name: "unique prefix", prefix: "c-ffff", want: "c-ffff0001"},
		{name: "exact id", prefix: "c-aaaa0003", want: "c-aaaa0003"},
		{name: "ambiguous prefix", prefix: "c-aaaa", wantErr: "matches 2 consults"},
		{name: "unknown prefix", prefix: "c-zzzz", wantErr: "no consult"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveConsult(records, tt.prefix)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveConsult: %v", err)
			}
			if got.ID != tt.want {
				t.Errorf("id = %q, want %q", got.ID, tt.want)
			}
		})
	}
}

func TestResolveConsultFallsBackToNewestWhenNoneRunning(t *testing.T) {
	records := []consult.Record{
		rec("c-newest", consult.StatusDone, 1),
		rec("c-older", consult.StatusFailed, 2),
	}
	got, err := resolveConsult(records, "")
	if err != nil {
		t.Fatalf("resolveConsult: %v", err)
	}
	if got.ID != "c-newest" {
		t.Errorf("id = %q, want the newest record", got.ID)
	}
}

func TestResolveConsultWithNoRecords(t *testing.T) {
	if _, err := resolveConsult(nil, ""); err == nil {
		t.Fatal("expected an error when nothing has been recorded")
	}
}

func TestFeedRendersEventsThroughTheHarness(t *testing.T) {
	var out bytes.Buffer
	feed := &consultFeed{out: &out, renderer: claude.Claude{}}

	lines := []string{
		`{"t":1.0,"d":{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"consult.go"}}]}}}`,
		`{"t":2.5,"d":{"type":"assistant","message":{"content":[{"type":"text","text":"Two lines\nof thought"}]}}}`,
		`{"t":63.0,"d":{"type":"result","result":"done","is_error":false}}`,
		`{"t":64.0,"raw":"codex: connection reset"}`,
	}
	for _, line := range lines {
		ev, ok := consult.DecodeEvent([]byte(line))
		if !ok {
			t.Fatalf("decoding %q", line)
		}
		feed.emit(ev)
	}

	got := out.String()
	for _, want := range []string{
		"0:01", "read", "consult.go",
		"0:02", "text", "Two lines",
		"1:03", "result", "done",
		"1:04", "raw", "codex: connection reset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("feed missing %q:\n%s", want, got)
		}
	}
	// A multi-line body keeps its continuation aligned under the first line.
	if !strings.Contains(got, "\n"+strings.Repeat(" ", feedIndent)+"of thought") {
		t.Errorf("continuation line not indented:\n%s", got)
	}
}

func TestFeedWithoutARendererFallsBackToRawJSON(t *testing.T) {
	var out bytes.Buffer
	feed := &consultFeed{out: &out}
	ev, _ := consult.DecodeEvent([]byte(`{"t":1.0,"d":{"type":"assistant"}}`))
	feed.emit(ev)
	if !strings.Contains(out.String(), `{"type":"assistant"}`) {
		t.Errorf("want the raw event, got:\n%s", out.String())
	}
}

func TestTailerYieldsOnlyCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c-1.ndjson")
	if err := os.WriteFile(path, []byte(`{"t":1.0,"raw":"first"}`+"\n"+`{"t":2.0,"ra`), 0o600); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	defer f.Close()

	tail := &streamTailer{f: f}
	events, err := tail.drain()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 || events[0].Raw != "first" {
		t.Fatalf("got %+v, want only the complete line", events)
	}

	// The torn line completes on the next append.
	appendTo(t, path, `w":"second"}`+"\n")
	events, err = tail.drain()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 || events[0].Raw != "second" {
		t.Fatalf("got %+v, want the completed line", events)
	}
}

func appendTo(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("appending: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("appending: %v", err)
	}
}

func TestWatchReplaysAFinishedConsultAndExits(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	record := rec("c-done0001", consult.StatusDone, 0)
	writeTestRecord(t, dir, record)
	if err := os.WriteFile(filepath.Join(dir, record.ID+".ndjson"),
		[]byte(`{"t":1.0,"raw":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	var out bytes.Buffer
	if err := watchConsult(t.Context(), state, "", &out); err != nil {
		t.Fatalf("watchConsult: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "c-done0001") || !strings.Contains(got, "hello") {
		t.Errorf("want the header and replayed stream, got:\n%s", got)
	}
	if !strings.Contains(got, "done") {
		t.Errorf("want the terminal status reported, got:\n%s", got)
	}
}

func TestWatchFollowsUntilTheConsultFinishes(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	record := rec("c-live0001", consult.StatusRunning, 0)
	writeTestRecord(t, dir, record)
	stream := filepath.Join(dir, record.ID+".ndjson")
	if err := os.WriteFile(stream, []byte(`{"t":1.0,"raw":"working"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	restore := consultPollInterval
	consultPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { consultPollInterval = restore })

	go func() {
		time.Sleep(20 * time.Millisecond)
		appendTo(t, stream, `{"t":2.0,"raw":"still working"}`+"\n")
		time.Sleep(20 * time.Millisecond)
		appendTo(t, stream, `{"t":3.0,"raw":"finished"}`+"\n")
		done := record
		done.Status = consult.StatusDone
		done.EndedAt = done.StartedAt.Add(3 * time.Second)
		writeTestRecord(t, dir, done)
	}()

	var out bytes.Buffer
	if err := watchConsult(t.Context(), state, "c-live", &out); err != nil {
		t.Fatalf("watchConsult: %v", err)
	}
	got := out.String()
	for _, want := range []string{"working", "still working", "finished"} {
		if !strings.Contains(got, want) {
			t.Errorf("feed missing %q:\n%s", want, got)
		}
	}
}

func TestListRendersRunningConsultsFirst(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestRecord(t, dir, rec("c-finished1", consult.StatusDone, 3))
	writeTestRecord(t, dir, rec("c-running01", consult.StatusRunning, 1))
	writeTestRecord(t, dir, rec("c-queued001", consult.StatusQueued, 2))

	var out bytes.Buffer
	if err := listConsults(state, false, &out); err != nil {
		t.Fatalf("listConsults: %v", err)
	}
	got := out.String()
	running := strings.Index(got, "c-running01")
	queued := strings.Index(got, "c-queued001")
	finished := strings.Index(got, "c-finished1")
	if running < 0 || queued < 0 || finished < 0 {
		t.Fatalf("missing rows:\n%s", got)
	}
	if running > queued || queued > finished {
		t.Errorf("want in-flight consults first, newest first:\n%s", got)
	}
}

func TestListJSONIsMachineReadable(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestRecord(t, dir, rec("c-json0001", consult.StatusDone, 0))

	var out bytes.Buffer
	if err := listConsults(state, true, &out); err != nil {
		t.Fatalf("listConsults: %v", err)
	}
	if !strings.Contains(out.String(), `"id": "c-json0001"`) {
		t.Errorf("want JSON records, got:\n%s", out.String())
	}
}

func TestListWithNothingRecorded(t *testing.T) {
	var out bytes.Buffer
	if err := listConsults(t.TempDir(), false, &out); err != nil {
		t.Fatalf("listConsults: %v", err)
	}
	if !strings.Contains(out.String(), "No consults") {
		t.Errorf("want an empty-state message, got:\n%s", out.String())
	}
}

func TestFormatOffset(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{1500 * time.Millisecond, "0:01"},
		{63 * time.Second, "1:03"},
		{3723 * time.Second, "1:02:03"},
	}
	for _, tt := range tests {
		if got := formatOffset(tt.in); got != tt.want {
			t.Errorf("formatOffset(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestWatchReportsAnAbandonedConsult covers the record a SIGKILLed daemon
// leaves behind: nothing will ever close it, so watch must not wait forever.
func TestWatchReportsAnAbandonedConsult(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stuck := rec("c-stuck0001", consult.StatusRunning, int((consult.StaleAfter+time.Minute)/time.Minute))
	writeTestRecord(t, dir, stuck)
	if err := os.WriteFile(filepath.Join(dir, stuck.ID+".ndjson"),
		[]byte(`{"t":1.0,"raw":"started"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	var out bytes.Buffer
	if err := watchConsult(t.Context(), state, "", &out); err != nil {
		t.Fatalf("watchConsult: %v", err)
	}
	if !strings.Contains(out.String(), "abandoned") {
		t.Errorf("want the consult reported as abandoned, got:\n%s", out.String())
	}
}

// TestWatchStopsWhenTheRecordVanishes guards against polling a stale
// in-memory copy forever when the record is pruned out from under us.
func TestWatchStopsWhenTheRecordVanishes(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	record := rec("c-vanish001", consult.StatusRunning, 0)
	writeTestRecord(t, dir, record)
	stream := filepath.Join(dir, record.ID+".ndjson")
	if err := os.WriteFile(stream, []byte(`{"t":1.0,"raw":"working"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seeding stream: %v", err)
	}

	restore := consultPollInterval
	consultPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { consultPollInterval = restore })

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = os.Remove(filepath.Join(dir, record.ID+".json"))
	}()

	done := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		done <- watchConsult(t.Context(), state, "c-vanish", &out)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error once the record is gone")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch never returned; it is polling a stale record")
	}
}

// TestListMarksAbandonedConsults: reporting "running" for a record nothing
// will ever update is a lie that never expires.
func TestListMarksAbandonedConsults(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "consults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestRecord(t, dir, rec("c-stuck0001", consult.StatusRunning,
		int((consult.StaleAfter+time.Minute)/time.Minute)))

	var out bytes.Buffer
	if err := listConsults(state, false, &out); err != nil {
		t.Fatalf("listConsults: %v", err)
	}
	if !strings.Contains(out.String(), "abandoned") {
		t.Errorf("want the stuck consult marked abandoned, got:\n%s", out.String())
	}
}
