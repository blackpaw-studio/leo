package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// PromptWatch follows a claude session transcript as claude appends to it
// and reports once a user prompt that is exactly a given text (the opening
// a launch carried on argv) has been written there: the ground truth that
// the prompt reached the conversation. Not safe for concurrent use.
type PromptWatch struct {
	path   string
	prompt string
	// offset is where the next unread line starts.
	offset int64
	found  bool
}

// NewPromptWatch watches the transcript at path for prompt, compared with
// surrounding whitespace trimmed: the brief rides argv through "$(cat …)",
// which drops its trailing newlines.
func NewPromptWatch(path, prompt string) *PromptWatch {
	return &PromptWatch{path: path, prompt: strings.TrimSpace(prompt)}
}

// Seen reads the transcript's complete lines written since the last call
// and reports whether the prompt is among the user prompts read so far. A
// transcript not written yet has not seen it; an empty prompt never is.
func (w *PromptWatch) Seen() (bool, error) {
	if w.found || w.prompt == "" {
		return w.found, nil
	}
	f, err := os.Open(w.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
		return false, fmt.Errorf("reading transcript: %w", err)
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A trailing partial line is claude mid-write: read it whole
			// next time.
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("reading transcript: %w", err)
		}
		w.offset += int64(len(line))
		if isUserPrompt(line, w.prompt) {
			w.found = true
			return true, nil
		}
	}
}

// userLinePrefilter skips lines that cannot be a user prompt without
// decoding them: transcripts carry large tool results.
var userLinePrefilter = []byte(`"user"`)

// isUserPrompt reports whether line is a user transcript entry whose text
// is prompt.
func isUserPrompt(line []byte, prompt string) bool {
	if !bytes.Contains(line, userLinePrefilter) {
		return false
	}
	var entry struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "user" {
		return false
	}
	return strings.TrimSpace(contentText(entry.Message.Content)) == prompt
}

// contentText is a message's text: content as a string, or its text blocks
// joined.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
