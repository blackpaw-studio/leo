package consult

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReadOutputRenderedLineTailAndPartialEnvelope(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := Record{ID: "d-output", Harness: "unknown", Kind: "dispatch", Status: StatusRunning, StartedAt: time.Now()}
	if err := writeRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	stream := strings.Join([]string{
		`{"t":1,"raw":"one\ntwo\nthree"}`,
		`{"t":2,"raw":"four"}`,
		`{"t":3,"raw":"ignored"`, // torn trailing envelope
	}, "\n")
	if err := os.WriteFile(StreamPath(state, rec.ID), []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadOutput(state, "d-output", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "d-output" || !got.Truncated {
		t.Fatalf("output = %+v", got)
	}
	want := []string{"                  three", "   0:02  raw      four"}
	if len(got.Lines) != len(want) {
		t.Fatalf("lines = %#v, want %#v", got.Lines, want)
	}
	for i := range want {
		if got.Lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got.Lines[i], want[i])
		}
	}
}

func TestReadOutputResolvesTurnAndRejectsMissingStream(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, Record{ID: "d-turn", Kind: "dispatch", Status: StatusIdle, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StreamPath(state, "d-turn"), []byte("{\"t\":0,\"raw\":\"ok\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadOutput(state, "d-turn#2", 60)
	if err != nil || got.ID != "d-turn" {
		t.Fatalf("output = %+v, err = %v", got, err)
	}
	if _, err := ReadOutput(state, "d-missing", 1); err == nil || !strings.Contains(err.Error(), "unknown dispatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ReadOutput(state, "../d-turn", 1); err == nil || !strings.Contains(err.Error(), "invalid dispatch id") {
		t.Fatalf("err = %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "d-turn.ndjson")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOutput(state, "d-turn", 1); err == nil || !strings.Contains(err.Error(), "recording unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestLimitWaitEntryUTF8(t *testing.T) {
	text := strings.Repeat("é", 20000)
	got := limitWaitEntry(Entry{ID: "d-utf8", Text: text, Err: text})
	for _, value := range []string{got.Text, got.Err} {
		if len(value) > maxWaitEntryBytes || !strings.Contains(value, "[truncated; collect recorded output") || !utf8.ValidString(value) {
			t.Fatalf("limited value invalid: bytes=%d valid=%t", len(value), utf8.ValidString(value))
		}
	}
}
