package consult

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultOutputTail = 60
	maxOutputTail     = 400
	maxWaitEntryBytes = 32768
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
	raw, err := os.ReadFile(StreamPath(stateDir, runID))
	if err != nil {
		return Output{}, fmt.Errorf("recording unavailable for dispatch %s", runID)
	}
	ring := make([]string, 0, tail)
	truncated := false
	renderer := NewRenderer("")
	// Render after resolving the record so persisted runs retain their harness mapping.
	rec, _ := LoadOne(stateDir, runID)
	renderer = NewRenderer(rec.Harness)
	for len(raw) > 0 {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(raw[:i], "\r")
		raw = raw[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		event, ok := DecodeEvent(line)
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
	return Output{ID: runID, Lines: ring, Truncated: truncated}, nil
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
	runID := strings.SplitN(id, "#", 2)[0]
	note := `[truncated; collect recorded output with leo_dispatch_output {"id":"` + runID + `"}]`
	available := maxWaitEntryBytes - len(note)
	if available <= 0 {
		return note[:maxWaitEntryBytes]
	}
	prefix := text[:min(available, len(text))]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + note
}
