package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractInvocationMarker(t *testing.T) {
	body := "<!-- leo:invocation=abcdef0123456789abcdef0123456789 -->\nhello"
	got := extractInvocationMarker(body)
	if got != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("got %q", got)
	}
	if extractInvocationMarker("plain") != "" {
		t.Fatalf("expected empty for no marker")
	}
}

func TestReadLastTurnPicksLatest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":[{"type":"text","text":"<!-- leo:invocation=00000000000000000000000000000aaa -->\nstart"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"sure"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"<!-- leo:invocation=00000000000000000000000000000bbb -->\nsecond"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"all done"}]}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	uid, final, err := readLastTurn(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if uid != "00000000000000000000000000000bbb" {
		t.Fatalf("uid: %q", uid)
	}
	if final != "all done" {
		t.Fatalf("final: %q", final)
	}
}

func TestReadLastTurnNoMarkerReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":[{"type":"text","text":"just a human turn"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	uid, final, err := readLastTurn(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if uid != "" {
		t.Fatalf("expected empty uid (human turn), got %q", uid)
	}
	if final != "" {
		t.Fatalf("expected empty final, got %q", final)
	}
}

func TestReadLastTurnSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	lines := []string{
		`not valid json`,
		`{"type":"user","message":{"content":[{"type":"text","text":"<!-- leo:invocation=00000000000000000000000000000aaa -->\nq"}]}}`,
		`also broken`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"reply"}]}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	uid, final, err := readLastTurn(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if uid != "00000000000000000000000000000aaa" || final != "reply" {
		t.Fatalf("uid=%q final=%q", uid, final)
	}
}
