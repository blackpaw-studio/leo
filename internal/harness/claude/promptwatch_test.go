package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func appendTranscript(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func seen(t *testing.T, w *PromptWatch) bool {
	t.Helper()
	ok, err := w.Seen()
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	return ok
}

// A transcript not written yet has not seen the prompt; one that has a user
// line carrying it has, whether claude wrote the content as a string or as
// text blocks. The brief rides argv through "$(cat …)", which drops its
// trailing newlines, so surrounding whitespace never matters.
func TestPromptWatchFindsAUserPrompt(t *testing.T) {
	for name, line := range map[string]string{
		"string content": `{"type":"user","message":{"role":"user","content":"do the task\nwell"}}`,
		"text blocks":    `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"do the task\nwell"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			w := NewPromptWatch(path, "do the task\nwell\n\n")
			if seen(t, w) {
				t.Fatal("a missing transcript saw the prompt")
			}
			appendTranscript(t, path, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"do the task\nwell"}]}}`+"\n")
			if seen(t, w) {
				t.Fatal("an assistant line counted as the prompt")
			}
			appendTranscript(t, path, line+"\n")
			if !seen(t, w) {
				t.Fatal("the user prompt was not seen")
			}
			if !seen(t, w) {
				t.Fatal("the user prompt was not kept seen")
			}
		})
	}
}

// claude appends to the transcript while it is read: a line is judged only
// once it is complete, and nothing read before is read again.
func TestPromptWatchReadsOnlyCompleteNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w := NewPromptWatch(path, "the opening")
	line := `{"type":"user","message":{"role":"user","content":"the opening"}}` + "\n"
	appendTranscript(t, path, `{"type":"user","message":{"role":"user","content":"something else"}}`+"\n"+line[:20])
	if seen(t, w) {
		t.Fatal("a partial line counted")
	}
	appendTranscript(t, path, line[20:])
	if !seen(t, w) {
		t.Fatal("the completed line was not seen")
	}
}

// claude records an argv prompt verbatim: a user line carrying part of it,
// or quoting it inside another message, is not the prompt arriving, nor is
// an empty brief ever "seen".
func TestPromptWatchNeedsThePromptItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	appendTranscript(t, path, `{"type":"user","message":{"role":"user","content":"the open"}}`+"\n")
	appendTranscript(t, path, `{"type":"user","message":{"role":"user","content":"earlier you were told: the opening"}}`+"\n")
	if seen(t, NewPromptWatch(path, "the opening")) {
		t.Fatal("part of the prompt, or a quote of it, counted")
	}
	appendTranscript(t, path, `{"type":"user","message":{"role":"user","content":""}}`+"\n")
	if seen(t, NewPromptWatch(path, "  \n")) {
		t.Fatal("an empty prompt counted")
	}
}
