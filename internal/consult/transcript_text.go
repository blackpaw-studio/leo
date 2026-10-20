package consult

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"
)

// transcriptTextLine is the part of a transcript line that carries an
// assistant message's text.
type transcriptTextLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// lastAssistantText follows a transcript's assistant messages and keeps the
// text of the latest one that has any. Streaming writes one message across
// several lines, a block per line, so a message's texts are joined.
type lastAssistantText struct {
	since time.Time
	id    string
	texts []string
}

func (l *lastAssistantText) line(raw []byte) {
	var entry transcriptTextLine
	if json.Unmarshal(raw, &entry) != nil || entry.Type != "assistant" || entry.Message == nil || entry.Timestamp.Before(l.since) {
		return
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(entry.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "text" || strings.TrimSpace(b.Text) == "" {
			continue
		}
		if entry.Message.ID != l.id {
			l.id, l.texts = entry.Message.ID, nil
		}
		l.texts = append(l.texts, b.Text)
	}
}

// lastAssistantTextSince is the text of the last assistant message written to
// the transcript at path at or after since: a turn's final reply, when the
// Stop that would have carried it was lost. A missing transcript has none.
func lastAssistantTextSince(read TranscriptReader, path string, since time.Time) (string, error) {
	last := lastAssistantText{since: since}
	var offset int64
	var partial []byte
	for {
		data, size, err := read(path, offset)
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		offset += int64(len(data))
		buf := append(append([]byte(nil), partial...), data...)
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			last.line(buf[:i])
			buf = buf[i+1:]
		}
		if len(buf) > maxTranscriptLine {
			buf = nil
		}
		partial = append([]byte(nil), buf...)
		if len(data) == 0 || offset >= size {
			break
		}
	}
	last.line(partial)
	return strings.Join(last.texts, "\n"), nil
}
