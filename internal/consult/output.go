package consult

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultOutputTail  = 60
	maxOutputTail      = 400
	maxWaitEntryBytes  = 32768
	outputReadBuffer   = 64 << 10
	maxWaitNoteIDBytes = 256
)

type Output struct {
	ID        string   `json:"id"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

func NormalizeOutputTail(tail int) (int, error) {
	if tail == 0 {
		return defaultOutputTail, nil
	}
	if tail < 0 {
		return 0, errors.New("tail must be a positive integer")
	}
	if tail > maxOutputTail {
		return maxOutputTail, nil
	}
	return tail, nil
}

// ReadOutput snapshots a dispatch stream without observing or collecting it.
func ReadOutput(stateDir, id string, tail int) (Output, error) {
	tail, err := NormalizeOutputTail(tail)
	if err != nil {
		return Output{}, err
	}
	runID, turn, hasTurn := strings.Cut(id, "#")
	if runID == "" || strings.ContainsAny(runID, `/\\`) || strings.Contains(turn, "#") || (hasTurn && (turn == "" || !positiveInteger(turn))) {
		return Output{}, fmt.Errorf("invalid dispatch id %q", id)
	}
	if _, err := LoadOne(stateDir, runID); err != nil {
		return Output{}, fmt.Errorf("unknown dispatch %s", id)
	}
	stream, err := os.Open(StreamPath(stateDir, runID))
	if err != nil {
		return Output{}, fmt.Errorf("recording unavailable for dispatch %s", runID)
	}
	defer stream.Close()
	// Render after resolving the record so persisted runs retain their harness mapping.
	rec, _ := LoadOne(stateDir, runID)
	lines, truncated, err := readOutputStream(stream, NewRenderer(rec.Harness), tail)
	if err != nil {
		return Output{}, fmt.Errorf("reading dispatch stream: %w", err)
	}
	return Output{ID: runID, Lines: lines, Truncated: truncated}, nil
}

// readOutputStream reads bounded complete frames. A frame exceeding the
// buffer is intentionally skipped: reassembling it would let a live stream
// dictate our memory use, and watch already skips unparseable frames.
func readOutputStream(reader io.Reader, renderer Renderer, tail int) ([]string, bool, error) {
	framed := bufio.NewReaderSize(reader, outputReadBuffer)
	ring := make([]string, 0, tail)
	truncated := false
	for {
		line, err := framed.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = framed.ReadSlice('\n')
			}
			if errors.Is(err, io.EOF) {
				return ring, truncated, nil
			}
			if err != nil {
				return nil, false, err
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return ring, truncated, nil
		} // torn trailing envelope
		if err != nil {
			return nil, false, err
		}
		lineText := strings.TrimRight(string(line), "\r\n")
		if strings.TrimSpace(lineText) == "" {
			continue
		}
		event, ok := DecodeEvent([]byte(lineText))
		if !ok {
			continue
		}
		for _, rendered := range renderer.Render(event) {
			rendered = StripControlSequences(rendered)
			if len(ring) == tail {
				copy(ring, ring[1:])
				ring[len(ring)-1] = rendered
				truncated = true
			} else {
				ring = append(ring, rendered)
			}
		}
	}
}

func positiveInteger(value string) bool { n, err := strconv.Atoi(value); return err == nil && n > 0 }

func limitWaitEntry(entry Entry) Entry {
	entry.Text = limitWaitText(entry.ID, entry.Text)
	entry.Err = limitWaitText(entry.ID, entry.Err)
	return entry
}

func limitWaitText(id, text string) string {
	if len(text) <= maxWaitEntryBytes {
		return text
	}
	runID := boundedWaitRunID(strings.SplitN(id, "#", 2)[0])
	note := truncateUTF8(`[truncated; collect recorded output with leo_dispatch_output {"id":"`+runID+`"}]`, maxWaitEntryBytes)
	available := maxWaitEntryBytes - len(note)
	if available <= 0 {
		return note
	}
	prefix := truncateUTF8(text, min(available, len(text)))
	return prefix + note
}

func boundedWaitRunID(id string) string {
	if id == "" || !utf8.ValidString(id) {
		return "unknown"
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return "unknown"
		}
	}
	return truncateUTF8(id, maxWaitNoteIDBytes)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
