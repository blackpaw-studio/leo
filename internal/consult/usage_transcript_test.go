package consult

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "transcript", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// memTranscripts serves transcripts from memory the way readTranscriptFrom
// serves them from disk.
type memTranscripts map[string][]byte

func (m memTranscripts) read(path string, offset int64) ([]byte, int64, error) {
	data, ok := m[path]
	if !ok {
		return nil, 0, fs.ErrNotExist
	}
	size := int64(len(data))
	if offset > size {
		return nil, size, nil
	}
	return data[offset:], size, nil
}

func wantTally(t *testing.T, tally *transcriptTally, in, out int64) {
	t.Helper()
	if gotIn, gotOut := tally.totals(); gotIn != in || gotOut != out {
		t.Fatalf("tally in=%d out=%d, want in=%d out=%d", gotIn, gotOut, in, out)
	}
}

// Streaming writes one assistant message across several lines, its output
// growing: the message counts once, at its latest usage, with input counting
// cache writes and reads.
func TestTranscriptTallyCountsEachMessageOnceAtItsLatestUsage(t *testing.T) {
	var tally transcriptTally
	tally.feed(fixture(t, "streamed.jsonl"))
	wantTally(t, &tally, 1110+1202, 50+7)
}

// A trailing line still being written is held back until it is complete.
func TestTranscriptTallyHoldsAPartialTrailingLine(t *testing.T) {
	data := fixture(t, "next_turn.jsonl")
	cut := len(data) - 20
	var tally transcriptTally
	tally.feed(data[:cut])
	wantTally(t, &tally, 0, 0)
	tally.feed(data[cut:])
	wantTally(t, &tally, 1323, 9)
}

// Lines that are not assistant messages with usage are skipped.
func TestTranscriptTallyIgnoresNoise(t *testing.T) {
	var tally transcriptTally
	tally.feed(fixture(t, "noise.jsonl"))
	tally.feed(fixture(t, "next_turn.jsonl"))
	wantTally(t, &tally, 1323, 9)
}

// Successive polls read only what was appended, and the totals accumulate.
func TestTranscriptTallyAccumulatesAcrossPolls(t *testing.T) {
	files := memTranscripts{"/t.jsonl": fixture(t, "streamed.jsonl")}
	var tally transcriptTally
	if err := tally.advance(files.read, "/t.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := tally.advance(files.read, "/t.jsonl"); err != nil {
		t.Fatal(err)
	}
	wantTally(t, &tally, 2312, 57)
	files["/t.jsonl"] = append(append([]byte{}, files["/t.jsonl"]...), fixture(t, "next_turn.jsonl")...)
	if err := tally.advance(files.read, "/t.jsonl"); err != nil {
		t.Fatal(err)
	}
	wantTally(t, &tally, 2312+1323, 57+9)
}

// A transcript that does not exist yet is not an error: nothing is counted.
func TestTranscriptTallyMissingTranscriptCountsNothing(t *testing.T) {
	var tally transcriptTally
	if err := tally.advance(memTranscripts{}.read, "/missing.jsonl"); err != nil {
		t.Fatalf("missing transcript: %v", err)
	}
	wantTally(t, &tally, 0, 0)
}

// A truncated transcript is re-read from the start without counting its
// messages twice, and a new session's transcript (after /clear) adds to the
// totals rather than replacing them.
func TestTranscriptTallySurvivesTruncationAndRotation(t *testing.T) {
	files := memTranscripts{"/a.jsonl": fixture(t, "streamed.jsonl")}
	var tally transcriptTally
	_ = tally.advance(files.read, "/a.jsonl")
	files["/a.jsonl"] = fixture(t, "streamed.jsonl")[:10]
	_ = tally.advance(files.read, "/a.jsonl")
	files["/a.jsonl"] = fixture(t, "streamed.jsonl")
	_ = tally.advance(files.read, "/a.jsonl")
	wantTally(t, &tally, 2312, 57)

	files["/b.jsonl"] = fixture(t, "next_turn.jsonl")
	_ = tally.advance(files.read, "/b.jsonl")
	wantTally(t, &tally, 2312+1323, 57+9)
}

// A read error other than a missing file is reported and changes nothing.
func TestTranscriptTallyReportsReadErrors(t *testing.T) {
	boom := errors.New("boom")
	var tally transcriptTally
	err := tally.advance(func(string, int64) ([]byte, int64, error) { return nil, 0, boom }, "/x.jsonl")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	wantTally(t, &tally, 0, 0)
}

// The disk reader returns what follows offset, and the file's size.
func TestReadTranscriptFromReadsTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, size, err := readTranscriptFrom(path, 2)
	if err != nil || string(data) != "cdef" || size != 6 {
		t.Fatalf("got %q size=%d err=%v", data, size, err)
	}
}
