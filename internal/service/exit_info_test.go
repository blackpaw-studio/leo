package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadExitCode(t *testing.T) {
	dir := t.TempDir()

	// missing file → not ok
	if code, ok := readExitCode(dir, "assistant"); ok || code != 0 {
		t.Errorf("missing file: want (0,false), got (%d,%v)", code, ok)
	}

	// valid integer (trailing newline)
	if err := os.WriteFile(filepath.Join(dir, "assistant-exit.code"), []byte("1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, ok := readExitCode(dir, "assistant")
	if !ok || code != 1 {
		t.Errorf("want (1,true), got (%d,%v)", code, ok)
	}

	// garbage → not ok
	if err := os.WriteFile(filepath.Join(dir, "assistant-exit.code"), []byte("not a number"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code, ok := readExitCode(dir, "assistant"); ok {
		t.Errorf("garbage: want not ok, got (%d,%v)", code, ok)
	}
}

func TestResetExitCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assistant-exit.code")

	// Seed a prior run's code, then reset before the next launch.
	if err := os.WriteFile(path, []byte("137\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resetExitCode(dir, "assistant")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
	// readExitCode must now report "unknown" rather than the stale 137.
	if code, ok := readExitCode(dir, "assistant"); ok || code != 0 {
		t.Errorf("after reset: want (0,false), got (%d,%v)", code, ok)
	}

	// Reset on missing file is a no-op (no panic, no error surfaced).
	resetExitCode(dir, "assistant")
}

func TestDecodeSignal(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, "none"},
		{1, "none"},
		{127, "none"},
		{128, "none"},
		{128 + int(syscall.SIGKILL), "SIGKILL"},
		{128 + int(syscall.SIGTERM), "SIGTERM"},
		{128 + int(syscall.SIGINT), "SIGINT"},
		{128 + int(syscall.SIGSEGV), "SIGSEGV"},
	}
	for _, tt := range tests {
		if got := decodeSignal(tt.code); got != tt.want {
			t.Errorf("decodeSignal(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
	// Uncommon signal falls back to numeric form
	if got := decodeSignal(128 + 99); !strings.HasPrefix(got, "signal=") {
		t.Errorf("decodeSignal(227) = %q, want signal=... fallback", got)
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")

	// missing → nil
	if got := tailLines(path, 5); got != nil {
		t.Errorf("missing: want nil, got %v", got)
	}

	// fewer lines than n → returns all
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := tailLines(path, 10)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// more lines than n → returns tail
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = string(rune('a' + (i % 26)))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got = tailLines(path, 5)
	if len(got) != 5 {
		t.Errorf("want 5 lines, got %d", len(got))
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("last tail line = %q, want %q", got[len(got)-1], lines[len(lines)-1])
	}

	// empty file → nil
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if got := tailLines(path, 5); got != nil {
		t.Errorf("empty: want nil, got %v", got)
	}
}

func TestWriteExitLog(t *testing.T) {
	dir := t.TempDir()
	err := writeExitLog(dir, "assistant", 137, true, "SIGKILL", 11*time.Minute+5*time.Second,
		[]string{"panic: something", "  at module.fn"})
	if err != nil {
		t.Fatalf("writeExitLog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "assistant-exit.log"))
	if err != nil {
		t.Fatalf("read exit log: %v", err)
	}
	content := string(data)
	for _, sub := range []string{
		"assistant: exit=137 signal=SIGKILL after 11m5s",
		"--- last 2 lines of stderr ---",
		"panic: something",
		"  at module.fn",
	} {
		if !strings.Contains(content, sub) {
			t.Errorf("exit log missing %q\nfull content:\n%s", sub, content)
		}
	}
}

func TestWriteExitLog_UnknownExit(t *testing.T) {
	dir := t.TempDir()
	if err := writeExitLog(dir, "p", 0, false, "none", time.Second, nil); err != nil {
		t.Fatalf("writeExitLog: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "p-exit.log"))
	if !strings.Contains(string(data), "exit=? signal=none") {
		t.Errorf("want exit=? placeholder when code unknown, got: %s", data)
	}
}

func TestAdvanceBackoff(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		elapsed time.Duration
		want    time.Duration
	}{
		{"quick exit doubles 5s→10s", 5 * time.Second, 2 * time.Second, 10 * time.Second},
		{"doubles 10s→20s", 10 * time.Second, 30 * time.Second, 20 * time.Second},
		{"doubles 20s→40s", 20 * time.Second, 2 * time.Minute, 40 * time.Second},
		{"caps at 60s", 40 * time.Second, 5 * time.Minute, 60 * time.Second},
		{"stays capped", 60 * time.Second, 5 * time.Minute, 60 * time.Second},
		{"resets on 10m exactly", 60 * time.Second, 10 * time.Minute, 5 * time.Second},
		{"resets after 30m", 60 * time.Second, 30 * time.Minute, 5 * time.Second},
		{"resets from any value", 40 * time.Second, 15 * time.Minute, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := advanceBackoff(tt.current, tt.elapsed); got != tt.want {
				t.Errorf("advanceBackoff(%s, %s) = %s, want %s", tt.current, tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestAdvanceBackoff_ConsecutiveFailuresPattern exercises the spec's concrete
// example: five consecutive fast crashes should grow 5→10→20→40→60→60.
func TestAdvanceBackoff_ConsecutiveFailuresPattern(t *testing.T) {
	b := initialBackoff // 5s
	want := []time.Duration{
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second,
		60 * time.Second, // capped
	}
	for i, w := range want {
		b = advanceBackoff(b, 30*time.Second) // fast crash each time
		if b != w {
			t.Errorf("iteration %d: got %s, want %s", i, b, w)
		}
	}
	// A long run should reset back to initialBackoff.
	if got := advanceBackoff(b, 20*time.Minute); got != initialBackoff {
		t.Errorf("after healthy run: got %s, want %s", got, initialBackoff)
	}
}

func TestLogProcessExit(t *testing.T) {
	var buf bytes.Buffer
	logProcessExit(&buf, "assistant", 11*time.Minute+5*time.Second, 10*time.Second,
		1, true, "none", "/state/assistant-exit.log", true)
	out := buf.String()
	wantMain := "[assistant] claude exited after 11m5s (exit=1, signal=none), restarting in 10s"
	if !strings.Contains(out, wantMain) {
		t.Errorf("main log line missing; got:\n%s", out)
	}
	if !strings.Contains(out, "last 50 lines of stderr written to /state/assistant-exit.log") {
		t.Errorf("hint line missing; got:\n%s", out)
	}
}

func TestLogProcessExit_NoTail(t *testing.T) {
	var buf bytes.Buffer
	logProcessExit(&buf, "p", time.Second, 5*time.Second, 0, false, "none", "/state/p-exit.log", false)
	out := buf.String()
	if !strings.Contains(out, "(exit=?, signal=none)") {
		t.Errorf("want exit=? placeholder; got:\n%s", out)
	}
	if strings.Contains(out, "last 50 lines") {
		t.Errorf("hint line should be absent when haveTail=false; got:\n%s", out)
	}
}
