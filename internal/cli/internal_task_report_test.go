package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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

// TestReadLastTurnHandlesStringContent reproduces the real Claude Code
// transcript shape: plain user prompts encode message.content as a bare
// string, not an array of typed blocks. The marker must still be found.
func TestReadLastTurnHandlesStringContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":"<!-- leo:invocation=00000000000000000000000000000ccc -->\nrun it"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done via string"}]}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	uid, final, err := readLastTurn(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if uid != "00000000000000000000000000000ccc" {
		t.Fatalf("uid: %q (string-encoded user content not parsed)", uid)
	}
	if final != "done via string" {
		t.Fatalf("final: %q", final)
	}
}

// TestReadLastTurnHandlesStringAssistantContent covers an assistant turn whose
// content is also a bare string.
func TestReadLastTurnHandlesStringAssistantContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":"<!-- leo:invocation=00000000000000000000000000000ddd -->\nq"}}`,
		`{"type":"assistant","message":{"content":"plain string reply"}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	uid, final, err := readLastTurn(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if uid != "00000000000000000000000000000ddd" {
		t.Fatalf("uid: %q", uid)
	}
	if final != "plain string reply" {
		t.Fatalf("final: %q", final)
	}
}

// TestReportHomeDirPrefersEnv verifies the Stop-hook reporter targets the
// daemon home from LEO_HOME (exported by the session supervisor) rather than
// always assuming the default home — otherwise a daemon on a non-default home
// never receives the report and the invocation times out.
func TestReportHomeDirPrefersEnv(t *testing.T) {
	t.Setenv("LEO_HOME", "/custom/leo/home")
	if got := reportHomeDir(); got != "/custom/leo/home" {
		t.Fatalf("got %q, want /custom/leo/home", got)
	}
}

func TestReportHomeDirFallsBackToDefault(t *testing.T) {
	t.Setenv("LEO_HOME", "")
	if got, want := reportHomeDir(), config.DefaultHome(); got != want {
		t.Fatalf("got %q, want default %q", got, want)
	}
}
