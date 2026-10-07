package consult

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// maxTranscriptRead bounds one poll's read of a transcript; the rest is
// read on the next poll.
const maxTranscriptRead = 8 << 20

// maxTranscriptLine bounds a held-back partial line. A longer one (a huge
// tool result still being written) is dropped: it never carries usage.
const maxTranscriptLine = 16 << 20

// TranscriptReader returns what follows offset in the transcript at path,
// and the file's current size. A missing transcript is fs.ErrNotExist.
type TranscriptReader func(path string, offset int64) (data []byte, size int64, err error)

// readTranscriptFrom is the TranscriptReader for transcripts on disk.
func readTranscriptFrom(path string, offset int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat transcript: %w", err)
	}
	if offset >= info.Size() {
		return nil, info.Size(), nil
	}
	data, err := io.ReadAll(io.NewSectionReader(f, offset, min(info.Size()-offset, maxTranscriptRead)))
	if err != nil {
		return nil, 0, fmt.Errorf("reading transcript: %w", err)
	}
	return data, info.Size(), nil
}

type messageTokens struct{ input, output int64 }

// transcriptTally sums the usage a claude session's transcripts report,
// read incrementally. Streaming writes one assistant message across several
// lines, so each message counts once, at its latest usage. Messages survive
// a change of transcript (a /clear starts a new one), so totals only grow.
type transcriptTally struct {
	path          string
	offset        int64
	partial       []byte
	messages      map[string]messageTokens
	input, output int64
}

func (t *transcriptTally) totals() (input, output int64) { return t.input, t.output }

// advance reads what was appended to the transcript at path since the last
// call. A transcript that does not exist yet counts nothing.
func (t *transcriptTally) advance(read TranscriptReader, path string) error {
	if path != t.path {
		t.path, t.offset, t.partial = path, 0, nil
	}
	data, size, err := read(path, t.offset)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if size < t.offset { // truncated or replaced: start over
		t.offset, t.partial = 0, nil
		if data, _, err = read(path, 0); err != nil {
			return err
		}
	}
	t.offset += int64(len(data))
	t.feed(data)
	return nil
}

// feed counts every complete line of data, holding back a trailing partial
// one until the rest of it arrives.
func (t *transcriptTally) feed(data []byte) {
	buf := append(append([]byte(nil), t.partial...), data...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		t.line(buf[:i])
		buf = buf[i+1:]
	}
	if len(buf) > maxTranscriptLine {
		buf = nil
	}
	t.partial = append([]byte(nil), buf...)
}

type transcriptLine struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	Message *struct {
		ID    string `json:"id"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (t *transcriptTally) line(raw []byte) {
	var l transcriptLine
	if json.Unmarshal(raw, &l) != nil || l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
		return
	}
	id := l.Message.ID
	if id == "" {
		id = "uuid:" + l.UUID
	}
	u := l.Message.Usage
	next := messageTokens{
		input:  max(u.Input, 0) + max(u.CacheCreation, 0) + max(u.CacheRead, 0),
		output: max(u.Output, 0),
	}
	if t.messages == nil {
		t.messages = map[string]messageTokens{}
	}
	prev := t.messages[id]
	t.messages[id] = next
	t.input += next.input - prev.input
	t.output += next.output - prev.output
}
