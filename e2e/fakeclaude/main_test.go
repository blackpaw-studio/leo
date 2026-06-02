package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendJSONLWritesNewlineDelimited(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tr.jsonl")
	appendJSONL(p, map[string]any{"type": "user", "x": 1})
	appendJSONL(p, map[string]any{"type": "assistant", "x": 2})
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(raw))
	}
	if !strings.Contains(lines[0], `"type":"user"`) || !strings.Contains(lines[1], `"type":"assistant"`) {
		t.Fatalf("event order wrong: %q", string(raw))
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hi", 80) != "hi" {
		t.Fatal("short string should pass through")
	}
	if truncate(strings.Repeat("x", 100), 80) != strings.Repeat("x", 80) {
		t.Fatal("long string should be truncated to 80")
	}
}

func TestWriteTranscriptUserSkipsEmptyPath(t *testing.T) {
	// Should not panic or create a file.
	writeTranscriptUser("", "x")
	writeTranscriptAssistant("", "x")
}

func TestRunInteractiveSubmissionEcho(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "tr.jsonl")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = outW
	t.Cleanup(func() { os.Stdout = origStdout })

	go func() {
		_, _ = w.Write([]byte("hello world\n\n"))
		_ = w.Close()
	}()

	runInteractive(transcript, "")
	_ = outW.Close()

	outBytes, _ := io.ReadAll(outR)
	if !strings.Contains(string(outBytes), "FAKE-REPLY: hello world") {
		t.Fatalf("expected echo, got %q", string(outBytes))
	}
	rawT, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(rawT), `"type":"user"`) || !strings.Contains(string(rawT), `"type":"assistant"`) {
		t.Fatalf("transcript missing events: %q", string(rawT))
	}
	if !strings.Contains(string(rawT), `"text":"hello world"`) {
		t.Fatalf("user text missing: %q", string(rawT))
	}
}

func TestRunInteractiveResumeEcho(t *testing.T) {
	r, w, _ := os.Pipe()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	outR, outW, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = outW
	t.Cleanup(func() { os.Stdout = origStdout })

	go func() {
		_ = w.Close()
	}()

	runInteractive("", "abc-123")
	_ = outW.Close()

	outBytes, _ := io.ReadAll(outR)
	if !strings.Contains(string(outBytes), "resumed: abc-123") {
		t.Fatalf("expected resume echo, got %q", string(outBytes))
	}
}

func TestRunInteractiveDrainsTrailingBufferOnEOF(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "tr.jsonl")

	r, w, _ := os.Pipe()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	outR, outW, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = outW
	t.Cleanup(func() { os.Stdout = origStdout })

	go func() {
		// No trailing blank line — EOF should still flush.
		_, _ = w.Write([]byte("trailing line\n"))
		_ = w.Close()
	}()

	runInteractive(transcript, "")
	_ = outW.Close()

	outBytes, _ := io.ReadAll(outR)
	if !strings.Contains(string(outBytes), "FAKE-REPLY: trailing line") {
		t.Fatalf("expected EOF flush, got %q", string(outBytes))
	}
}

func TestHasFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bare", []string{"--interactive"}, true},
		{"equals", []string{"--interactive=true"}, true},
		{"absent", []string{"--model", "sonnet"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasFlag(tc.args, "--interactive"); got != tc.want {
				t.Fatalf("hasFlag(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestFilterKnownFlags(t *testing.T) {
	args := []string{
		"--model", "sonnet",
		"--interactive",
		"--transcript-path", "/tmp/x.jsonl",
		"--append-system-prompt", "foo",
		"--resume", "abc",
		"--unknown=value",
	}
	got := filterKnownFlags(args, []string{"interactive", "transcript-path", "resume"})
	want := []string{
		"--interactive",
		"--transcript-path", "/tmp/x.jsonl",
		"--resume", "abc",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("filterKnownFlags mismatch:\n got=%v\nwant=%v", got, want)
	}
}
